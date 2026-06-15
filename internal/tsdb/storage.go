package tsdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oil-tank-radar/gateway/internal/config"
	"github.com/oil-tank-radar/gateway/pkg/model"
	"go.uber.org/zap"
)

type Storage struct {
	cfg         config.TimeScaleDBConfig
	pool        *pgxpool.Pool
	logger      *zap.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	batchCh     chan *model.LevelMeasurement
	batch       []*model.LevelMeasurement
	batchSize   int
	mu          sync.Mutex
	flushTicker *time.Ticker
	stats       StorageStats
}

type StorageStats struct {
	MeasurementsWritten uint64
	BatchesFlushed      uint64
	WriteErrors         uint64
	QueryErrors         uint64
}

func NewStorage(cfg config.TimeScaleDBConfig, logger *zap.Logger) *Storage {
	ctx, cancel := context.WithCancel(context.Background())
	return &Storage{
		cfg:       cfg,
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
		batchCh:   make(chan *model.LevelMeasurement, cfg.BatchSize*2),
		batch:     make([]*model.LevelMeasurement, 0, cfg.BatchSize),
		batchSize: cfg.BatchSize,
	}
}

func (s *Storage) Start() error {
	connStr := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		s.cfg.User, s.cfg.Password, s.cfg.Host, s.cfg.Port,
		s.cfg.Database, s.cfg.SSLMode,
	)

	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("parse pool config: %w", err)
	}

	poolConfig.MaxConns = int32(s.cfg.MaxConns)
	poolConfig.MinConns = int32(s.cfg.MinConns)
	poolConfig.MaxConnIdleTime = s.cfg.ConnMaxIdleTime

	s.pool, err = pgxpool.NewWithConfig(s.ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}

	if err := s.pool.Ping(s.ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	if err := s.initSchema(); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	s.flushTicker = time.NewTicker(s.cfg.FlushInterval)
	s.wg.Add(1)
	go s.batchWriter()

	s.logger.Info("TimeScaleDB storage started",
		zap.String("host", s.cfg.Host),
		zap.Int("port", s.cfg.Port),
		zap.String("database", s.cfg.Database),
		zap.String("hypertable", s.cfg.HyperTable),
	)

	return nil
}

func (s *Storage) initSchema() error {
	hyperTable := s.cfg.HyperTable

	createTableSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			time TIMESTAMPTZ NOT NULL,
			frame_number BIGINT NOT NULL,
			level_m DOUBLE PRECISION NOT NULL,
			velocity_mps DOUBLE PRECISION,
			wave_height_m DOUBLE PRECISION,
			snr_db DOUBLE PRECISION,
			temperature_c DOUBLE PRECISION,
			distance_m DOUBLE PRECISION,
			volume_m3 DOUBLE PRECISION,
			confidence DOUBLE PRECISION,
			raw_data BYTEA
		)
	`, hyperTable)

	_, err := s.pool.Exec(s.ctx, createTableSQL)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	createHypertableSQL := fmt.Sprintf(
		"SELECT create_hypertable('%s', 'time', if_not_exists => TRUE)",
		hyperTable,
	)
	_, err = s.pool.Exec(s.ctx, createHypertableSQL)
	if err != nil {
		return fmt.Errorf("create hypertable: %w", err)
	}

	createIndexSQL := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS idx_%s_frame_number ON %s (frame_number DESC)",
		hyperTable, hyperTable,
	)
	_, err = s.pool.Exec(s.ctx, createIndexSQL)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	if s.cfg.EnableContinuousAggregates {
		aggView := fmt.Sprintf("%s_1h", hyperTable)
		createAggSQL := fmt.Sprintf(`
			CREATE MATERIALIZED VIEW IF NOT EXISTS %s
			WITH (timescaledb.continuous) AS
			SELECT
				time_bucket('1 hour', time) AS bucket,
				AVG(level_m) AS avg_level_m,
				MIN(level_m) AS min_level_m,
				MAX(level_m) AS max_level_m,
				AVG(wave_height_m) AS avg_wave_height_m,
				COUNT(*) AS sample_count
			FROM %s
			GROUP BY bucket
			WITH NO DATA
		`, aggView, hyperTable)

		_, err = s.pool.Exec(s.ctx, createAggSQL)
		if err != nil {
			s.logger.Warn("Failed to create continuous aggregate", zap.Error(err))
		}
	}

	return nil
}

func (s *Storage) batchWriter() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			s.flushBatch()
			return
		case <-s.flushTicker.C:
			s.flushBatch()
		case m, ok := <-s.batchCh:
			if !ok {
				s.flushBatch()
				return
			}

			s.mu.Lock()
			s.batch = append(s.batch, m)
			if len(s.batch) >= s.batchSize {
				s.flushBatchLocked()
			}
			s.mu.Unlock()
		}
	}
}

func (s *Storage) flushBatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushBatchLocked()
}

func (s *Storage) flushBatchLocked() {
	if len(s.batch) == 0 {
		return
	}

	hyperTable := s.cfg.HyperTable
	rows := make([][]interface{}, 0, len(s.batch))

	for _, m := range s.batch {
		m.SNRdB = m.SNR
		row := []interface{}{
			m.Timestamp,
			m.FrameNumber,
			m.LevelM,
			m.VelocityMPS,
			m.WaveHeightM,
			m.SNRdB,
			m.TemperatureC,
			m.DistanceM,
			m.VolumeM3,
			m.Confidence,
			m.RawData,
		}
		rows = append(rows, row)
	}

	copyCols := []string{
		"time", "frame_number", "level_m", "velocity_mps", "wave_height_m",
		"snr_db", "temperature_c", "distance_m", "volume_m3", "confidence", "raw_data",
	}

	conn, err := s.pool.Acquire(s.ctx)
	if err != nil {
		s.logger.Error("Failed to acquire connection for batch write", zap.Error(err))
		s.stats.WriteErrors += uint64(len(s.batch))
		s.batch = s.batch[:0]
		return
	}
	defer conn.Release()

	copyCount, err := conn.Conn().CopyFrom(
		s.ctx,
		pgx.Identifier{hyperTable},
		copyCols,
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		s.logger.Error("Batch copy failed", zap.Error(err), zap.Int("batch_size", len(s.batch)))
		s.stats.WriteErrors += uint64(len(s.batch))
	} else {
		s.stats.MeasurementsWritten += uint64(copyCount)
		s.stats.BatchesFlushed++
	}

	for _, m := range s.batch {
		m.Unref()
	}
	s.batch = s.batch[:0]
}

func (s *Storage) WriteMeasurement(m *model.LevelMeasurement) error {
	if m == nil {
		return errors.New("nil measurement")
	}

	m.Ref()

	select {
	case s.batchCh <- m:
		return nil
	case <-s.ctx.Done():
		m.Unref()
		return s.ctx.Err()
	default:
		m.Unref()
		s.stats.WriteErrors++
		return errors.New("write buffer full")
	}
}

func (s *Storage) QueryRange(ctx context.Context, start, end time.Time, limit int) ([]*model.LevelMeasurement, error) {
	if end.Before(start) {
		return nil, errors.New("end time before start time")
	}

	hyperTable := s.cfg.HyperTable

	querySQL := fmt.Sprintf(`
		SELECT time, frame_number, level_m, velocity_mps, wave_height_m,
			   snr_db, temperature_c, distance_m, volume_m3, confidence, raw_data
		FROM %s
		WHERE time >= $1 AND time <= $2
		ORDER BY time DESC
		LIMIT $3
	`, hyperTable)

	rows, err := s.pool.Query(ctx, querySQL, start, end, limit)
	if err != nil {
		s.stats.QueryErrors++
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var measurements []*model.LevelMeasurement

	for rows.Next() {
		m := &model.LevelMeasurement{}
		m.Ref()
		var rawData []byte

		err := rows.Scan(
			&m.Timestamp,
			&m.FrameNumber,
			&m.LevelM,
			&m.VelocityMPS,
			&m.WaveHeightM,
			&m.SNRdB,
			&m.TemperatureC,
			&m.DistanceM,
			&m.VolumeM3,
			&m.Confidence,
			&rawData,
		)
		if err != nil {
			s.stats.QueryErrors++
			return nil, fmt.Errorf("scan row: %w", err)
		}

		m.SNR = m.SNRdB
		m.RawData = rawData
		measurements = append(measurements, m)
	}

	if err := rows.Err(); err != nil {
		s.stats.QueryErrors++
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return measurements, nil
}

func (s *Storage) GetLatest(ctx context.Context, tankID string) (*model.LevelMeasurement, error) {
	measurements, err := s.QueryRange(ctx, time.Now().Add(-24*time.Hour), time.Now(), 1)
	if err != nil {
		return nil, err
	}

	if len(measurements) == 0 {
		return nil, errors.New("no data found")
	}

	return measurements[0], nil
}

func (s *Storage) GetStats() StorageStats {
	return s.stats
}

func (s *Storage) Close() error {
	s.cancel()
	close(s.batchCh)
	s.wg.Wait()

	if s.flushTicker != nil {
		s.flushTicker.Stop()
	}

	if s.pool != nil {
		s.pool.Close()
	}

	stats := s.GetStats()
	s.logger.Info("TimeScaleDB storage stopped",
		zap.Uint64("measurements_written", stats.MeasurementsWritten),
		zap.Uint64("batches_flushed", stats.BatchesFlushed),
		zap.Uint64("write_errors", stats.WriteErrors),
		zap.Uint64("query_errors", stats.QueryErrors),
	)

	return nil
}
