package config

import "time"

type Config struct {
UDP        UDPConfig        `yaml:"udp"`
Framer     FramerConfig     `yaml:"framer"`
FFT        FFTConfig        `yaml:"fft"`
Ranging    RangingConfig    `yaml:"ranging"`
TimeScaleDB TimeScaleDBConfig `yaml:"timescaledb"`
Runtime    RuntimeConfig    `yaml:"runtime"`
}

type UDPConfig struct {
ListenAddr      string        `yaml:"listen_addr"`
Port            int           `yaml:"port"`
RecvBufSize     int           `yaml:"recv_buf_size"`
MaxPacketSize   int           `yaml:"max_packet_size"`
EnableGRO       bool          `yaml:"enable_gro"`
EnableReusePort bool          `yaml:"enable_reuse_port"`
ReadTimeout     time.Duration `yaml:"read_timeout"`
IdleTimeout     time.Duration `yaml:"idle_timeout"`
Workers         int           `yaml:"workers"`
}

type FramerConfig struct {
ADCBits          int           `yaml:"adc_bits"`
SamplesPerChirp  int           `yaml:"samples_per_chirp"`
ChirpsPerFrame   int           `yaml:"chirps_per_frame"`
RxChannels       int           `yaml:"rx_channels"`
FrameHeaderSize  int           `yaml:"frame_header_size"`
FrameSize        int           `yaml:"frame_size"`
ChirpDuration    time.Duration `yaml:"chirp_duration"`
FramePeriod      time.Duration `yaml:"frame_period"`
FrameSyncTimeout time.Duration `yaml:"frame_sync_timeout"`
MaxFrameSize     int           `yaml:"max_frame_size"`
}

type FFTConfig struct {
RangeFFTSize   int     `yaml:"range_fft_size"`
DopplerFFTSize int     `yaml:"doppler_fft_size"`
WindowType     string  `yaml:"window_type"`
WindowAlpha    float64 `yaml:"window_alpha"`
CFARGuardCells int     `yaml:"cfar_guard_cells"`
CFARTrainCells int     `yaml:"cfar_train_cells"`
CFARThreshold  float64 `yaml:"cfar_threshold"`
Workers        int     `yaml:"workers"`
MaxQueueDepth  int     `yaml:"max_queue_depth"`
EnableFFTShift bool    `yaml:"enable_fft_shift"`
}

type RangingConfig struct {
StartFreqGHz           float64       `yaml:"start_freq_ghz"`
BandwidthGHz           float64       `yaml:"bandwidth_ghz"`
SampleRateMHz          float64       `yaml:"sample_rate_mhz"`
TankHeightM            float64       `yaml:"tank_height_m"`
MinDistanceM           float64       `yaml:"min_distance_m"`
MaxDistanceM           float64       `yaml:"max_distance_m"`
SNRThreshold           float64       `yaml:"snr_threshold"`
TempCompEnabled        bool          `yaml:"temp_comp_enabled"`
TempSensorAddr         string        `yaml:"temp_sensor_addr"`
TankDiameterM          float64       `yaml:"tank_diameter_m"`
TemperatureCoeff       float64       `yaml:"temperature_coeff"`
EnableTemperatureComp  bool          `yaml:"enable_temperature_compensation"`
PhaseUnwrapThreshold   float64       `yaml:"phase_unwrap_threshold"`
MaxVelocityMS          float64       `yaml:"max_velocity_m_s"`
WaveCalcWindow         int           `yaml:"wave_calc_window"`
}

type TimeScaleDBConfig struct {
Host                     string        `yaml:"host"`
Port                     int           `yaml:"port"`
User                     string        `yaml:"user"`
Password                 string        `yaml:"password"`
Database                 string        `yaml:"database"`
SSLMode                  string        `yaml:"ssl_mode"`
MaxConns                 int32         `yaml:"max_conns"`
MinConns                 int32         `yaml:"min_conns"`
ConnMaxLifetime          time.Duration `yaml:"conn_max_lifetime"`
ConnMaxIdleTime          time.Duration `yaml:"conn_max_idle_time"`
BatchSize                int           `yaml:"batch_size"`
FlushInterval            time.Duration `yaml:"flush_interval"`
HyperTable               string        `yaml:"hyper_table"`
ContinuousAggs           []string      `yaml:"continuous_aggs"`
EnableContinuousAggregates bool        `yaml:"enable_continuous_aggregates"`
AggregationInterval      string        `yaml:"aggregation_interval"`
}

type RuntimeConfig struct {
LogLevel         string        `yaml:"log_level"`
MetricsAddr      string        `yaml:"metrics_addr"`
PprofAddr        string        `yaml:"pprof_addr"`
ShutdownTimeout  time.Duration `yaml:"shutdown_timeout"`
BufferPoolSize   int           `yaml:"buffer_pool_size"`
EnableProfiling  bool          `yaml:"enable_profiling"`
EnablePrometheus bool          `yaml:"enable_prometheus"`
GracePeriod      time.Duration `yaml:"grace_period"`
}