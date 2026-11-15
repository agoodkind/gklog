package telemetry

import (
	"log/slog"
	"os"
	"strconv"
)

// LogSettingsFromEnv reads startup settings for logging.
//
// Missing variables default to enabled INFO logging. Explicit invalid values
// return errors, including empty strings.
//
// Example:
//
//	settings, err := LogSettingsFromEnv()
func LogSettingsFromEnv() (LogSettings, error) {
	enabled, err := boolFromEnv("GKLOG_LOG_ENABLED", true)
	if err != nil {
		return LogSettings{}, err
	}

	settings := LogSettings{Enabled: enabled, Level: slog.LevelInfo}
	if value, present := os.LookupEnv("GKLOG_LOG_LEVEL"); present {
		if err := settings.Level.UnmarshalText([]byte(value)); err != nil {
			return LogSettings{}, &operationError{operation: "parse GKLOG_LOG_LEVEL", err: err}
		}

		if err := settings.validate(); err != nil {
			return LogSettings{}, &operationError{operation: "parse GKLOG_LOG_LEVEL", err: err}
		}
	}

	return settings, nil
}

// TraceSettingsFromEnv reads startup settings for root sampling.
//
// Missing variables default to enabled full sampling. Explicit invalid values
// return errors, including empty strings.
//
// Example:
//
//	settings, err := TraceSettingsFromEnv()
func TraceSettingsFromEnv() (TraceSettings, error) {
	enabled, err := boolFromEnv("GKLOG_TRACE_ENABLED", true)
	if err != nil {
		return TraceSettings{}, err
	}

	settings := TraceSettings{Enabled: enabled, SampleRatio: 1}
	if value, present := os.LookupEnv("GKLOG_TRACE_SAMPLE_RATIO"); present {
		ratio, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return TraceSettings{}, &operationError{
				operation: "parse GKLOG_TRACE_SAMPLE_RATIO",
				err:       err,
			}
		}

		settings.SampleRatio = ratio
		if err := settings.validate(); err != nil {
			return TraceSettings{}, &operationError{
				operation: "parse GKLOG_TRACE_SAMPLE_RATIO",
				err:       err,
			}
		}
	}

	return settings, nil
}

// ProfileEnabledFromEnv reads GKLOG_CPU_PROFILE_ENABLED and defaults to false.
//
// Explicit empty or invalid values return an error.
//
// Example:
//
//	enabled, err := ProfileEnabledFromEnv()
func ProfileEnabledFromEnv() (bool, error) {
	return boolFromEnv("GKLOG_CPU_PROFILE_ENABLED", false)
}

// MetricExportEnabledFromEnv reads GKLOG_METRIC_EXPORT_ENABLED.
//
// Missing values default to false. Explicit empty or invalid values return
// an error.
//
// Example:
//
//	enabled, err := MetricExportEnabledFromEnv()
func MetricExportEnabledFromEnv() (bool, error) {
	return boolFromEnv("GKLOG_METRIC_EXPORT_ENABLED", false)
}

func boolFromEnv(key string, defaultValue bool) (bool, error) {
	value, present := os.LookupEnv(key)
	if !present {
		return defaultValue, nil
	}

	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, &operationError{operation: "parse " + key, err: err}
	}

	return enabled, nil
}
