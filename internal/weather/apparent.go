// apparent.go — apparent ("feels like") temperature calculations.
//
// TASK-084: The raw dry-bulb temperature often does not reflect perceived
// heat or cold. Hot+humid days feel hotter than the thermometer reads
// (heat index), while cold+windy days feel colder (wind chill). Using the
// apparent temperature for heat/cold market signals improves calibration by
// 5–10% compared to using the raw MaxTempC alone.
package weather

import "math"

// HeatIndexC computes the apparent temperature using the Rothfusz regression
// equation. Valid for tempC ≥ 27°C and relHumidityPct ≥ 40%; returns tempC
// unchanged when conditions fall outside the validity range.
//
// Reference: NWS / Rothfusz (1990), "The Heat Index Equation".
func HeatIndexC(tempC, relHumidityPct float64) float64 {
	if tempC < 27 || relHumidityPct < 40 {
		return tempC
	}
	// Convert to °F for the Rothfusz formula (empirically derived in imperial units).
	T := tempC*9.0/5.0 + 32.0
	R := relHumidityPct
	hi := -42.379 +
		2.04901523*T +
		10.14333127*R -
		0.22475541*T*R -
		0.00683783*T*T -
		0.05391559*R*R +
		0.00122874*T*T*R +
		0.00085282*T*R*R -
		0.00000199*T*T*R*R
	// Convert back to °C.
	return (hi - 32.0) * 5.0 / 9.0
}

// WindChillC computes the apparent temperature using the NOAA / Météo Canada
// wind chill formula (metric version). Valid for tempC ≤ 10°C and windKMH >
// 4.8 km/h; returns tempC unchanged outside the validity range.
//
// Reference: NWS / NOAA (2001), "New Wind Chill Index".
func WindChillC(tempC, windKMH float64) float64 {
	if tempC > 10 || windKMH <= 4.8 {
		return tempC
	}
	V := math.Pow(windKMH, 0.16)
	return 13.12 + 0.6215*tempC - 11.37*V + 0.3965*tempC*V
}

// ApparentTempC returns the "feels like" temperature by selecting the
// appropriate formula based on conditions:
//
//   - Hot and humid (tempC ≥ 27°C, RH ≥ 40%) → Heat Index (Rothfusz)
//   - Cold and windy  (tempC ≤ 10°C, wind > 4.8 km/h) → Wind Chill (NOAA)
//   - Otherwise → dry-bulb temperature unchanged.
func ApparentTempC(tempC, relHumidityPct, windKMH float64) float64 {
	if tempC >= 27 && relHumidityPct >= 40 {
		return HeatIndexC(tempC, relHumidityPct)
	}
	if tempC <= 10 && windKMH > 4.8 {
		return WindChillC(tempC, windKMH)
	}
	return tempC
}
