// Package platform reports build-dependent limits.
package platform

// Probe checks that the platform limits are usable.
func Probe() error {
	_, err := MaxBatch()
	return err
}
