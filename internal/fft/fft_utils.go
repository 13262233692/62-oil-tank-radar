package fft

func RangeBinToDistance(rangeBin, rangeBins, samplesPerChirp, chirpsPerFrame, fftSize int, startFreqGHz, bandwidthGHz, sampleRateMHz float64) float64 {
	c := 299792458.0
	bandwidthHz := bandwidthGHz * 1e9

	binResolution := c / (2 * bandwidthHz)
	nyquistRange := binResolution * float64(fftSize) / 2

	return float64(rangeBin) * nyquistRange / float64(fftSize/2)
}

func DopplerBinToVelocity(dopplerBin, dopplerBins int, startFreqGHz float64, sampleRateHz float64, samplesPerChirp int) float64 {
	c := 299792458.0
	startFreqHz := startFreqGHz * 1e9
	wavelength := c / startFreqHz
	prf := sampleRateHz / float64(samplesPerChirp)

	velocityResolution := wavelength * prf / (2 * float64(dopplerBins))
	nyquistVelocity := velocityResolution * float64(dopplerBins) / 2

	return (float64(dopplerBin) - float64(dopplerBins)/2) * nyquistVelocity / float64(dopplerBins/2)
}