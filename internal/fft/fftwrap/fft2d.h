#ifndef FFT2D_H
#define FFT2D_H

#include <stdint.h>
#include <complex.h>

#define WINDOW_HANN       0
#define WINDOW_HAMMING    1
#define WINDOW_BLACKMAN   2
#define WINDOW_KAISER     3
#define WINDOW_RECTANGULAR 4

typedef double complex complex128_t;

typedef struct {
    int range_fft_size;
    int doppler_fft_size;
    int window_type;
    double window_alpha;
    int cfar_guard_cells;
    int cfar_train_cells;
    double cfar_threshold;
} FFTConfig;

typedef struct {
    int range_bin;
    int doppler_bin;
    double magnitude;
    double phase;
    double snr;
    double distance_m;
    double velocity_mps;
    double confidence;
} PeakInfoC;

typedef struct {
    PeakInfoC* peaks;
    int num_peaks;
    int max_peaks;
} PeakDetectionResult;

int fft_init(FFTConfig* config);
void fft_cleanup(void);

void fft_1d(complex128_t* data, int n, int inverse);
void fft_2d(complex128_t* data, int rows, int cols, int inverse);

void window_apply(complex128_t* data, int n, int window_type, double alpha);
void window_generate(double* window, int n, int window_type, double alpha);

void range_doppler_fft(
    const int16_t* input,
    complex128_t* output,
    int num_chirps,
    int num_samples,
    int num_channels,
    FFTConfig* config
);

void cfar_detector(
    const complex128_t* range_doppler,
    int range_bins,
    int doppler_bins,
    PeakDetectionResult* result,
    FFTConfig* config
);

void phase_unwrap(
    const double* input_phases,
    double* output_phases,
    int n
);

void magnitude_spectrum(
    const complex128_t* input,
    double* output,
    int n
);

void log_magnitude(
    const double* input,
    double* output,
    int n,
    double ref
);

int next_power_of_two(int n);

void fft_shift(
    complex128_t* data,
    int rows,
    int cols
);

void zero_pad(
    const complex128_t* input,
    complex128_t* output,
    int input_rows,
    int input_cols,
    int output_rows,
    int output_cols
);

#endif