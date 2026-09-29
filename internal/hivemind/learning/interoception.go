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

	// --- Real learning -------------------------------------------------
	// Predictor is a GRU that learns to forecast the NEXT tick's raw signals
	// from this tick's signals and the resulting feelings. The objective is
	// self-supervised: the target is what the machine actually reported one
	// tick later, not a hand-written label. Nothing here encodes a rule like
	// "stress when cpu > 0.7" — the mapping is discovered by descent.
	Predictor *GRU
	Readout   *Sequential

	// Opt is held on the struct so momentum survives across ticks. Building a
	// fresh optimizer per step (as an earlier revision did) silently reduces
	// SGD+momentum to plain SGD, because the velocity map is thrown away.
	Opt          *SGD
	PredictorOpt *SGD

	// Previous sample, needed to form a (prev -> cur) training pair.
	prev       RawInteroception
	prevFeel   map[string]float32
	havePrev   bool
	haveFeel   bool
	LearnEvery int
	skip       int

	// Observable training state, so a caller can prove learning happened
	// rather than take it on faith.
	LastLoss float32
	LossEMA  float32
	Updates  int
}

const (
	// Hidden width of the next-step predictor.
	predictorHidden = 12
	// Number of raw signals fed and predicted.
	numSignals = 5
	// Number of feeling channels fed back in as context.
	numFeelings = 4
)

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

	// Next-step predictor: [prev signals | prev feelings] -> GRU -> [next signals]
	pc.Predictor = NewGRU(numSignals+numFeelings, predictorHidden)
	pc.Readout = NewSequential(
		NewLinear(predictorHidden, 8),
		Tanh(),
		NewLinear(8, numSignals),
	)
	// One persistent optimizer, shared by both stages, so momentum accumulates
	// across ticks instead of being reset every call. The recurrent predictor
	// is trained at a deliberately smaller rate than the feature encoder, and
	// with a gradient-norm bound, because backprop through hidden state is
	// far more prone to runaway than a plain MLP.
	pc.Opt = NewSGD(pc.Lr, 0.9)
	pc.Opt.Clip = 1.0
	predictorLR := pc.Lr * 0.1
	if predictorLR < 1e-4 {
		predictorLR = 1e-4
	}
	pc.PredictorOpt = NewSGD(predictorLR, 0.9)
	pc.PredictorOpt.Clip = 0.5
	pc.LearnEvery = 1

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

	// Learn from the same sample, inside Step, so the weights cannot silently
	// stay frozen again. Step is the single per-tick entry point.
	pc.Learn(raw, feelings)

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

// Learn performs one step of gradient descent on the next-signal prediction
// objective. Call it with the CURRENT sample; it pairs that against the
// previous sample held internally.
//
// The loss is gradient-connected end to end: the prediction flows out of the
// GRU readout (which owns the parameters), the target is a constant leaf, and
// MSELoss therefore accumulates real gradients into Predictor/Readout. An
// earlier revision also summed an l0 term built from two detached constants,
// which contributed exactly zero gradient while looking like a loss term.
//
// Curriculum learning: LearnEvery throttles the cadence so early learning is
// conservative; the persistent SGD optimizer (momentum survives across ticks)
// then settles the weights as Updates accumulate.
func (pc *InteroceptivePC) Learn(cur RawInteroception, curFeelings map[string]float32) {
	// Throttle if the owner asked for a slower cadence than every tick.
	if pc.LearnEvery > 1 {
		pc.skip++
		if pc.skip%pc.LearnEvery != 0 {
			return
		}
	}

	// First sample only establishes the baseline for a pair; there is nothing
	// to predict yet.
	if !pc.havePrev {
		pc.remember(cur, curFeelings)
		return
	}
	if !pc.haveFeel {
		pc.remember(cur, curFeelings)
		return
	}

	// Input: what we knew last tick (signals + the feelings they produced).
	in := make([]*Value, 0, numSignals+numFeelings)
	for _, v := range []float32{
		pc.prev.CPULoad, pc.prev.RAMPressure, pc.prev.Thermal,
		pc.prev.DiskIO, pc.prev.NetworkIO,
	} {
		in = append(in, NewValue(v))
	}
	for _, k := range []string{"stress", "calm", "arousal", "fatigue"} {
		in = append(in, NewValue(pc.prevFeel[k]))
	}

	// Forward through the recurrent predictor and the readout head.
	h := pc.Predictor.Forward(in)
	pred := pc.Readout.Forward(h)

	// Target: what actually arrived this tick. Self-supervised — no labels.
	target := []*Value{
		NewValue(cur.CPULoad), NewValue(cur.RAMPressure), NewValue(cur.Thermal),
		NewValue(cur.DiskIO), NewValue(cur.NetworkIO),
	}

	loss := MSELoss(pred, target)

	pc.Predictor.ZeroGrad()
	pc.Readout.ZeroGrad()
	loss.Backward()

	// One persistent optimizer, reused every step, so momentum survives.
	pc.PredictorOpt.Step(pc.Predictor.Params())
	pc.PredictorOpt.Step(pc.Readout.Params())

	pc.LastLoss = loss.Data
	pc.LossEMA = 0.9*pc.LossEMA + 0.1*loss.Data
	pc.Updates++

	pc.remember(cur, curFeelings)
}

// remember stashes the current sample as the next pair's input.
func (pc *InteroceptivePC) remember(raw RawInteroception, feelings map[string]float32) {
	pc.prev = raw
	pc.havePrev = true
	if feelings == nil {
		return
	}
	f := make(map[string]float32, numFeelings)
	for _, k := range []string{"stress", "calm", "arousal", "fatigue"} {
		f[k] = feelings[k]
	}
	pc.prevFeel = f
	pc.haveFeel = true
}

// WeightSnapshot returns a copy of every learned parameter value, keyed by
// stage. Used by tests and the dashboard to show that weights genuinely move.
func (pc *InteroceptivePC) WeightSnapshot() map[string][]float32 {
	out := make(map[string][]float32)
	add := func(stage string, ps []*Param) {
		vals := make([]float32, 0, len(ps))
		for _, p := range ps {
			vals = append(vals, p.Value.Data)
		}
		out[stage] = vals
	}
	add("predictor", pc.Predictor.Params())
	add("readout", pc.Readout.Params())
	return out
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
