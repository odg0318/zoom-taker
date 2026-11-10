package pipeline

import "testing"

func TestEstimateWith(t *testing.T) {
	cases := []struct {
		factor, audio float64
		passes        int
		want          float64
	}{
		{0.5, 100, 1, 60},  // metal: 10 + 1*100*0.5
		{0.5, 100, 2, 110}, // metal combo: 10 + 2*100*0.5
		{1.5, 100, 1, 160}, // cpu: 10 + 1*100*1.5
		{1.5, 0, 1, 10},    // zero audio = just the model load
	}
	for _, c := range cases {
		if got := estimateWith(c.factor, c.audio, c.passes); got != c.want {
			t.Errorf("estimateWith(%v, %v, %d) = %v, want %v", c.factor, c.audio, c.passes, got, c.want)
		}
	}
}

func TestFormatEstimate(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{0, "0s"},
		{17.2, "17s"},
		{45, "45s"},
		{170, "2m50s"},
		{3600, "1h0m0s"},
	}
	for _, c := range cases {
		if got := FormatEstimate(c.seconds); got != c.want {
			t.Errorf("FormatEstimate(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

func TestEstimateSecondsSane(t *testing.T) {
	// Monotonic in audio length and passes; never below the load overhead.
	if EstimateSeconds(0, 1) < estModelLoadSeconds {
		t.Error("estimate below model-load floor")
	}
	if EstimateSeconds(100, 2) <= EstimateSeconds(100, 1) {
		t.Error("combo passes must not reduce the estimate")
	}
	if EstimateSeconds(200, 1) <= EstimateSeconds(100, 1) {
		t.Error("estimate must grow with audio length")
	}
}
