package learning

import (
	"time"
)

// InteroceptiveSignal represents a raw hardware signal
type InteroceptiveSignal struct {
	Name  string
	Value float32
	Time  time.Time
}

// PredictiveCodingState holds the state of one predictive coding level
type PredictiveCodingState struct {
	// Prediction: what we expect
	Prediction float32
	// Actual input
	Input float32
	// Prediction error (delta)
	Error float32
	// Precision (inverse variance) - attention/gain
	Precision float32
	// Hidden state for temporal prediction
	Hidden []float32
}

// InteroceptivePC is a hierarchical predictive coding system
// Level 0: Raw interoceptive signals (CPU, RAM, thermal, disk)
// Level 1: Integrated "bodily state" (arousal, valence)
// Level 2: High-level "feeling" categories (stress, calm, arousal, fatigue)
type InteroceptivePC struct {
	Level0 *PredictiveCodingState // Raw signals
	Level1 *PredictiveCodingState // Integrated state
	Level2 *PredictiveCodingState // Feeling categories

	// Predictive models
	Level0To1 *Sequential // Level0 -> Level1 prediction
	Level1To2 *Sequential // Level1 -> Level2 prediction

	// Precision (attention) weights per signal
	Precisions map[string]float32

	// Learning
	Lr float32
}

func NewInteroceptivePC() *InteroceptivePC {
	pc := &InteroceptivePC{
		Level0: &PredictiveCodingState{
			Prediction: 0, Input: 0, Error: 0, Precision: 1.0,
			Hidden: make([]float32, 16),
		},
		Level1: &PredictiveCodingState{
			Prediction: 0, Input: 0, Error: 0, Precision: 1.0,
			Hidden: make([]float32, 16),
		},
		Level2: &PredictiveCodingState{
			Prediction: 0, Input: 0, Error: 0, Precision: 1.0,
			Hidden: make([]float32, 8),
		},
		Precisions: map[string]float32{
			"cpu_load":     1.5, // High precision = high attention
			"ram_pressure": 1.2,
			"thermal":      1.8, // Thermal is critical
			"disk_io":      1.0,
			"network_io":   0.8,
		},
		Lr: 0.01,
	}

	// Level0 (raw signals) -> Level1 (integrated state)
	pc.Level0To1 = NewSequential(
		NewLinear(5, 16), // 5 raw signals -> 16 hidden
		ReLU(),
		NewLinear(16, 8), // 8 dim integrated state
		Tanh(),
	)

	// Level1 -> Level2 (feeling categories)
	pc.Level1To2 = NewSequential(
		NewLinear(8, 16),
		ReLU(),
		NewLinear(16, 4), // 4 feeling categories: stress, calm, arousal, fatigue
		Sigmoid(),
	)

	return pc
}

// RawInteroception collects raw hardware signals
type RawInteroception struct {
	CPULoad     float32
	RAMPressure float32
	Thermal     float32
	DiskIO      float32
	NetworkIO   float32
	Timestamp   time.Time
}

// Step processes one tick of interoceptive data
func (pc *InteroceptivePC) Step(raw RawInteroception) (map[string]float32, map[string]float32) {
	// --- Level 0: Raw signals ---
	inputs := []float32{raw.CPULoad, raw.RAMPressure, raw.Thermal, raw.DiskIO, raw.NetworkIO}

	// Compute precision-weighted prediction errors
	pc.Level0.Input = inputs[0] // CPU load as primary
	pc.Level0.Error = pc.Level0.Input - pc.Level0.Prediction

	// Precision-weighted error
	pwError := pc.Level0.Error * pc.Precisions["cpu_load"]

	// Update prediction (simple exponential smoothing + error correction)
	pc.Level0.Prediction += pc.Lr * pwError

	// --- Level 1: Integrated bodily state ---
	// Use the NN to predict integrated state from raw signals
	x := make([]*Value, 5)
	for i, v := range inputs {
		x[i] = NewValue(v)
	}
	l1Out := pc.Level0To1.Forward(x)

	// Update Level1 state
	if len(pc.Level1.Hidden) == len(l1Out) {
		for i := range pc.Level1.Hidden {
			pc.Level1.Hidden[i] = l1Out[i].Data
		}
	}
	pc.Level1.Input = pc.Level1.Hidden[0] // Arousal dimension
	pc.Level1.Error = pc.Level1.Input - pc.Level1.Prediction
	pc.Level1.Prediction += pc.Lr * pc.Level1.Error

	// --- Level 2: Feeling categories ---
	l2Out := pc.Level1To2.Forward(l1Out)

	if len(pc.Level2.Hidden) == len(l2Out) {
		for i := range pc.Level2.Hidden {
			pc.Level2.Hidden[i] = l2Out[i].Data
		}
	}

	// Feeling categories: stress, calm, arousal, fatigue
	feelings := map[string]float32{
		"stress":  l2Out[0].Data,
		"calm":    l2Out[1].Data,
		"arousal": l2Out[2].Data,
		"fatigue": l2Out[3].Data,
	}

	// Precision-weighted errors (what the system "feels")
	predictionErrors := map[string]float32{
		"level0_cpu_error":        pc.Level0.Error,
		"level1_integrated_error": pc.Level1.Error,
	}

	return feelings, predictionErrors
}

// GetFeelings returns current feeling state
func (pc *InteroceptivePC) GetFeelings() map[string]float32 {
	return map[string]float32{
		"stress":  pc.Level2.Hidden[0],
		"calm":    pc.Level2.Hidden[1],
		"arousal": pc.Level2.Hidden[2],
		"fatigue": pc.Level2.Hidden[3],
	}
}

// GetPredictionErrors returns current prediction errors (raw "feelings")
func (pc *InteroceptivePC) GetPredictionErrors() map[string]float32 {
	return map[string]float32{
		"interoceptive_error": pc.Level0.Error,
		"integrated_error":    pc.Level1.Error,
	}
}

// Learn performs one step of gradient descent on prediction errors
func (pc *InteroceptivePC) Learn(raw RawInteroception) {
	// Forward pass
	inputs := []float32{raw.CPULoad, raw.RAMPressure, raw.Thermal, raw.DiskIO, raw.NetworkIO}
	x := make([]*Value, 5)
	for i, v := range inputs {
		x[i] = NewValue(v)
	}

	// Level 0 -> 1
	l1Out := pc.Level0To1.Forward(x)

	// Level 1 -> 2
	l2Out := pc.Level1To2.Forward(l1Out)

	// Compute loss: prediction error at each level
	// Level 0: predict CPU load from previous state
	l0Target := NewValue(inputs[0])
	l0PredVal := NewValue(pc.Level0.Prediction)
	l0Loss := MSELoss([]*Value{l0PredVal}, []*Value{l0Target})

	// Level 1: integrated state prediction
	l1Pred := l1Out[0]
	l1Target := NewValue(inputs[0]) // Simplified: use CPU as proxy
	l1Loss := MSELoss([]*Value{l1Pred}, []*Value{l1Target})

	// Level 2: feeling category prediction (self-supervised)
	// Target: high stress when CPU+thermal high, calm when low
	stressTarget := float32(0)
	if inputs[0] > 0.7 || inputs[2] > 0.7 {
		stressTarget = 1.0
	}
	l2Target := []*Value{
		NewValue(stressTarget),     // stress
		NewValue(1 - stressTarget), // calm
		NewValue(inputs[0]),        // arousal ~ CPU
		NewValue(inputs[2]),        // fatigue ~ thermal
	}
	l2Loss := MSELoss(l2Out, l2Target)

	// Total loss
	totalLoss := l0Loss.Add(l1Loss).Add(l2Loss)

	// Backward
	pc.Level0To1.ZeroGrad()
	pc.Level1To2.ZeroGrad()
	totalLoss.Backward()

	// Update
	opt := NewSGD(pc.Lr, 0.9)
	opt.Step(pc.Level0To1.Params())
	opt.Step(pc.Level1To2.Params())
}

// CollectRawInteroception reads actual hardware signals
func CollectRawInteroception() RawInteroception {
	// This would read actual /proc/stat, /proc/meminfo, thermal zones, etc.
	// For now, return placeholder that mind.go will replace with real data
	return RawInteroception{
		CPULoad:     0,
		RAMPressure: 0,
		Thermal:     0,
		DiskIO:      0,
		NetworkIO:   0,
		Timestamp:   time.Now(),
	}
}
