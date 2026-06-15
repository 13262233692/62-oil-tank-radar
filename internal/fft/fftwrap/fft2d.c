#include "fft2d.h"
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <stdio.h>

#ifndef M_PI
#define M_PI 3.14159265358979323846
#endif

static complex128_t* twiddle_factors = NULL;
static int twiddle_size = 0;
static double* window_cache = NULL;
static int window_cache_size = 0;
static int window_cache_type = -1;
static double window_cache_alpha = 0.0;

int fft_init(FFTConfig* config) {
    int max_size = config->range_fft_size > config->doppler_fft_size ?
                   config->range_fft_size : config->doppler_fft_size;

    twiddle_size = next_power_of_two(max_size);
    twiddle_factors = (complex128_t*)malloc(twiddle_size * sizeof(complex128_t));
    if (!twiddle_factors) {
        return -1;
    }

    for (int i = 0; i < twiddle_size; i++) {
        double angle = -2.0 * M_PI * i / twiddle_size;
        twiddle_factors[i] = cos(angle) + sin(angle) * I;
    }

    return 0;
}

void fft_cleanup(void) {
    if (twiddle_factors) {
        free(twiddle_factors);
        twiddle_factors = NULL;
    }
    if (window_cache) {
        free(window_cache);
        window_cache = NULL;
    }
    twiddle_size = 0;
    window_cache_size = 0;
    window_cache_type = -1;
}

static int bit_reverse(int n, int bits) {
    int reversed = 0;
    for (int i = 0; i < bits; i++) {
        reversed = (reversed << 1) | (n & 1);
        n >>= 1;
    }
    return reversed;
}

static void fft_cooley_tukey(complex128_t* data, int n, int inverse) {
    if (n <= 1) return;

    int bits = 0;
    while ((1 << bits) < n) bits++;

    for (int i = 0; i < n; i++) {
        int j = bit_reverse(i, bits);
        if (i < j) {
            complex128_t temp = data[i];
            data[i] = data[j];
            data[j] = temp;
        }
    }

    for (int s = 1; s <= bits; s++) {
        int m = 1 << s;
        int m_half = m >> 1;

        double angle_sign = inverse ? 2.0 : -2.0;
        complex128_t w_m = cos(angle_sign * M_PI / m) + sin(angle_sign * M_PI / m) * I;

        for (int k = 0; k < n; k += m) {
            complex128_t w = 1.0;
            for (int j = 0; j < m_half; j++) {
                complex128_t t = w * data[k + j + m_half];
                complex128_t u = data[k + j];
                data[k + j] = u + t;
                data[k + j + m_half] = u - t;
                w *= w_m;
            }
        }
    }

    if (inverse) {
        double norm = 1.0 / n;
        for (int i = 0; i < n; i++) {
            data[i] *= norm;
        }
    }
}

void fft_1d(complex128_t* data, int n, int inverse) {
    fft_cooley_tukey(data, n, inverse);
}

void fft_2d(complex128_t* data, int rows, int cols, int inverse) {
    for (int i = 0; i < rows; i++) {
        fft_1d(&data[i * cols], cols, inverse);
    }

    complex128_t* col = (complex128_t*)malloc(rows * sizeof(complex128_t));
    if (!col) return;

    for (int j = 0; j < cols; j++) {
        for (int i = 0; i < rows; i++) {
            col[i] = data[i * cols + j];
        }

        fft_1d(col, rows, inverse);

        for (int i = 0; i < rows; i++) {
            data[i * cols + j] = col[i];
        }
    }

    free(col);
}

void window_generate(double* window, int n, int window_type, double alpha) {
    switch (window_type) {
        case WINDOW_HANN:
            for (int i = 0; i < n; i++) {
                window[i] = 0.5 * (1.0 - cos(2.0 * M_PI * i / (n - 1)));
            }
            break;

        case WINDOW_HAMMING:
            for (int i = 0; i < n; i++) {
                window[i] = 0.54 - 0.46 * cos(2.0 * M_PI * i / (n - 1)));
            }
            break;

        case WINDOW_BLACKMAN:
            for (int i = 0; i < n; i++) {
                double t = 2.0 * M_PI * i / (n - 1));
                window[i] = 0.42 - 0.5 * cos(t) + 0.08 * cos(2.0 * t));
            }
            break;

        case WINDOW_KAISER: {
            double beta = alpha * M_PI;
            double denom = 1.0;
            for (int i = 1; i <= 20; i++) {
                double fact = 1.0;
                for (int j = 1; j <= i; j++) fact *= j;
                double term = pow(beta * beta / 4.0, i) / (fact * fact);
                denom += term;
            }

            for (int i = 0; i < n; i++) {
                double x = 2.0 * i / (n - 1) - 1.0;
                double arg = beta * sqrt(1.0 - x * x));
                double num = 1.0;
                for (int k = 1; k <= 20; k++) {
                    double fact = 1.0;
                    for (int j = 1; j <= k; j++) fact *= j;
                    double term = pow(arg * arg / 4.0, k) / (fact * fact);
                    num += term;
                }
                window[i] = num / denom;
            }
            break;
        }

        case WINDOW_RECTANGULAR:
        default:
            for (int i = 0; i < n; i++) {
                window[i] = 1.0;
            }
            break;
    }

    double sum = 0.0;
    for (int i = 0; i < n; i++) {
        sum += window[i] * window[i];
    }
    double norm = sqrt(n / sum);
    for (int i = 0; i < n; i++) {
        window[i] *= norm;
    }
}

void window_apply(complex128_t* data, int n, int window_type, double alpha) {
    if (window_cache_size != n || window_cache_type != window_type ||
        fabs(window_cache_alpha - alpha) > 1e-10) {
        if (window_cache) {
            free(window_cache));
        }
        window_cache = (double*)malloc(n * sizeof(double));
        if (!window_cache) return;

        window_generate(window_cache, n, window_type, alpha);
        window_cache_size = n;
        window_cache_type = window_type;
        window_cache_alpha = alpha;
    }

    for (int i = 0; i < n; i++) {
        data[i] *= window_cache[i];
    }
}

void range_doppler_fft(
    const int16_t* input,
    complex128_t* output,
    int num_chirps,
    int num_samples,
    int num_channels,
    FFTConfig* config
) {
    int range_fft_size = config->range_fft_size;
    int doppler_fft_size = config->doppler_fft_size;

    complex128_t* range_line = (complex128_t*)malloc(range_fft_size * sizeof(complex128_t));
    if (!range_line) return;

    complex128_t* doppler_line = (complex128_t*)malloc(doppler_fft_size * sizeof(complex128_t));
    if (!doppler_line) {
        free(range_line);
        return;
    }

    for (int ch = 0; ch < num_channels; ch++) {
        for (int chirp = 0; chirp < num_chirps; chirp++) {
            int input_offset = (ch * num_chirps + chirp) * num_samples;

            for (int i = 0; i < range_fft_size; i++) {
                if (i < num_samples) {
                    range_line[i] = (double)input[input_offset + i];
                } else {
                    range_line[i] = 0.0;
                }
            }

            window_apply(range_line, num_samples, config->window_type, config->window_alpha);
            fft_1d(range_line, range_fft_size, 0);

            for (int range_bin = 0; range_bin < range_fft_size; range_bin++) {
                int output_idx = (chirp * num_channels + ch) * range_fft_size + range_bin;
                output[output_idx] = range_line[range_bin];
            }
        }
    }

    for (int ch = 0; ch < num_channels; ch++) {
        for (int range_bin = 0; range_bin < range_fft_size; range_bin++) {
            for (int chirp = 0; chirp < doppler_fft_size; chirp++) {
                if (chirp < num_chirps) {
                    int input_idx = (chirp * num_channels + ch) * range_fft_size + range_bin;
                    doppler_line[chirp] = output[input_idx];
                } else {
                    doppler_line[chirp] = 0.0;
                }
            }

            window_apply(doppler_line, num_chirps, config->window_type, config->window_alpha);
            fft_1d(doppler_line, doppler_fft_size, 0);

            for (int doppler_bin = 0; doppler_bin < doppler_fft_size; doppler_bin++) {
                int output_idx = (doppler_bin * num_channels + ch) * range_fft_size + range_bin;
                output[output_idx] = doppler_line[doppler_bin];
            }
        }
    }

    free(range_line);
    free(doppler_line);
}

void cfar_detector(
    const complex128_t* range_doppler,
    int range_bins,
    int doppler_bins,
    PeakDetectionResult* result,
    FFTConfig* config
) {
    int guard_cells = config->cfar_guard_cells;
    int train_cells = config->cfar_train_cells;
    double threshold = config->cfar_threshold;

    double* magnitude = (double*)malloc(range_bins * doppler_bins * sizeof(double));
    if (!magnitude) return;

    for (int i = 0; i < range_bins * doppler_bins; i++) {
        magnitude[i] = cabs(range_doppler[i]);
    }

    result->num_peaks = 0;

    for (int r = guard_cells + train_cells; r < range_bins - guard_cells - train_cells; r++) {
        for (int d = guard_cells + train_cells; d < doppler_bins - guard_cells - train_cells; d++) {
            int center_idx = d * range_bins + r;
            double center_mag = magnitude[center_idx];

            double sum = 0.0;
            int count = 0;

            for (int dr = -guard_cells - train_cells; dr <= guard_cells + train_cells; dr++) {
                for (int dd = -guard_cells - train_cells; dd <= guard_cells + train_cells; dd++) {
                    if (abs(dr) <= guard_cells && abs(dd) <= guard_cells) {
                        continue;
                    }

                    int idx = (d + dd) * range_bins + (r + dr);
                    sum += magnitude[idx];
                    count++;
                }
            }

            double noise_floor = sum / count;
            double snr = center_mag / (noise_floor + 1e-10);

            if (snr > threshold) {
                int is_local_max = 1;
                for (int dr = -1; dr <= 1 && is_local_max; dr++) {
                    for (int dd = -1; dd <= 1 && is_local_max; dd++) {
                        if (dr == 0 && dd == 0) continue;
                        int idx = (d + dd) * range_bins + (r + dr);
                        if (magnitude[idx] >= center_mag) {
                            is_local_max = 0;
                        }
                    }
                }

                if (is_local_max && result->num_peaks < result->max_peaks) {
                    PeakInfoC* peak = &result->peaks[result->num_peaks];
                    peak->range_bin = r;
                    peak->doppler_bin = d;
                    peak->magnitude = center_mag;
                    peak->phase = carg(range_doppler[center_idx]);
                    peak->snr = snr;
                    peak->distance_m = 0.0;
                    peak->velocity_mps = 0.0;
                    peak->confidence = snr / (snr + 10.0);
                    result->num_peaks++;
                }
            }
        }
    }

    for (int i = 0; i < result->num_peaks - 1; i++) {
        for (int j = i + 1; j < result->num_peaks; j++) {
            if (result->peaks[j].snr > result->peaks[i].snr) {
                PeakInfoC temp = result->peaks[i];
                result->peaks[i] = result->peaks[j];
                result->peaks[j] = temp;
            }
        }
    }

    free(magnitude);
}

void phase_unwrap(
    const double* input_phases,
    double* output_phases,
    int n
) {
    if (n <= 0) return;

    output_phases[0] = input_phases[0];
    double phase_prev = output_phases[0];

    for (int i = 1; i < n; i++) {
        double phase_diff = input_phases[i] - input_phases[i - 1];

        while (phase_diff > M_PI) {
            phase_diff -= 2.0 * M_PI;
        }
        while (phase_diff < -M_PI) {
            phase_diff += 2.0 * M_PI;
        }

        output_phases[i] = phase_prev + phase_diff;
        phase_prev = output_phases[i];
    }
}

void magnitude_spectrum(
    const complex128_t* input,
    double* output,
    int n
) {
    for (int i = 0; i < n; i++) {
        output[i] = cabs(input[i]);
    }
}

void log_magnitude(
    const double* input,
    double* output,
    int n,
    double ref
) {
    for (int i = 0; i < n; i++) {
        double val = input[i] / (ref + 1e-20);
        output[i] = 20.0 * log10(val + 1e-20);
    }
}

int next_power_of_two(int n) {
    int power = 1;
    while (power < n) {
        power <<= 1;
    }
    return power;
}

void fft_shift(
    complex128_t* data,
    int rows,
    int cols
) {
    int half_rows = rows / 2;
    int half_cols = cols / 2;

    for (int i = 0; i < half_rows; i++) {
        for (int j = 0; j < cols; j++) {
            int idx1 = i * cols + j;
            int idx2 = (i + half_rows) * cols + j;
            complex128_t temp = data[idx1];
            data[idx1] = data[idx2];
            data[idx2] = temp;
        }
    }

    for (int i = 0; i < rows; i++) {
        for (int j = 0; j < half_cols; j++) {
            int idx1 = i * cols + j;
            int idx2 = i * cols + j + half_cols;
            complex128_t temp = data[idx1];
            data[idx1] = data[idx2];
            data[idx2] = temp;
        }
    }
}

void zero_pad(
    const complex128_t* input,
    complex128_t* output,
    int input_rows,
    int input_cols,
    int output_rows,
    int output_cols
) {
    memset(output, 0, output_rows * output_cols * sizeof(complex128_t));

    for (int i = 0; i < input_rows && i < output_rows; i++) {
        for (int j = 0; j < input_cols && j < output_cols; j++) {
            output[i * output_cols + j] = input[i * input_cols + j];
        }
    }
}