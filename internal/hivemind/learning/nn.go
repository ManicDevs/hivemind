package learning

import (
	"math"
	"math/rand"
)

// Tensor is a flat row-major tensor with shape
type Tensor struct {
	Data  []float32
	Shape []int
}

func NewTensor(shape ...int) *Tensor {
	size := 1
	for _, s := range shape {
		size *= s
	}
	return &Tensor{
		Data:  make([]float32, size),
		Shape: shape,
	}
}

func (t *Tensor) At(idx ...int) float32 {
	idxVal := 0
	stride := 1
	for i := len(t.Shape) - 1; i >= 0; i-- {
		idxVal += idx[i] * stride
		stride *= t.Shape[i]
	}
	return t.Data[idxVal]
}

func (t *Tensor) Set(v float32, idx ...int) {
	idxVal := 0
	stride := 1
	for i := len(t.Shape) - 1; i >= 0; i-- {
		idxVal += idx[i] * stride
		stride *= t.Shape[i]
	}
	t.Data[idxVal] = v
}

func (t *Tensor) Rows() int { return t.Shape[0] }
func (t *Tensor) Cols() int { return t.Shape[1] }
func (t *Tensor) Len() int  { return len(t.Data) }
func (t *Tensor) Clone() *Tensor {
	return &Tensor{Data: append([]float32{}, t.Data...), Shape: append([]int{}, t.Shape...)}
}

// ============================================================
// Autograd: Value + Grad
// ============================================================

type Value struct {
	Data float32
	Grad float32
	// for backward
	prev     []*Value
	op       string
	backward func()
}

func NewValue(data float32) *Value {
	return &Value{Data: data}
}

func (v *Value) Add(other *Value) *Value {
	out := NewValue(v.Data + other.Data)
	out.prev = []*Value{v, other}
	out.op = "+"
	out.backward = func() {
		v.Grad += out.Grad
		other.Grad += out.Grad
	}
	return out
}

func (v *Value) Mul(other *Value) *Value {
	out := NewValue(v.Data * other.Data)
	out.prev = []*Value{v, other}
	out.op = "*"
	out.backward = func() {
		v.Grad += other.Data * out.Grad
		other.Grad += v.Data * out.Grad
	}
	return out
}

func (v *Value) Tanh() *Value {
	t := float32(math.Tanh(float64(v.Data)))
	out := NewValue(t)
	out.prev = []*Value{v}
	out.op = "tanh"
	out.backward = func() {
		v.Grad += (1 - t*t) * out.Grad
	}
	return out
}

func (v *Value) Sigmoid() *Value {
	s := float32(1.0 / (1.0 + math.Exp(-float64(v.Data))))
	out := NewValue(s)
	out.prev = []*Value{v}
	out.op = "sigmoid"
	out.backward = func() {
		v.Grad += s * (1 - s) * out.Grad
	}
	return out
}

func (v *Value) Linear(w *Value, b *Value) *Value {
	out := v.Mul(w)
	if b != nil {
		out = out.Add(b)
	}
	return out
}

func (v *Value) Backward() {
	v.Grad = 1.0
	// topological order
	visited := make(map[*Value]bool)
	var topo []*Value
	var build func(*Value)
	build = func(v *Value) {
		if visited[v] {
			return
		}
		visited[v] = true
		for _, p := range v.prev {
			build(p)
		}
		topo = append(topo, v)
	}
	build(v)
	for i := len(topo) - 1; i >= 0; i-- {
		topo[i].backward()
	}
}

func (v *Value) ZeroGrad() {
	v.Grad = 0
	for _, p := range v.prev {
		p.ZeroGrad()
	}
}

// ============================================================
// Parameter: trainable Value
// ============================================================

type Param struct {
	*Value
}

func NewParam(data float32) *Param {
	return &Param{NewValue(data)}
}

func (p *Param) ZeroGrad() { p.Value.ZeroGrad() }

// ============================================================
// Linear Layer
// ============================================================

type Linear struct {
	Weight  [][]*Param // [out][in]
	Bias    []*Param   // [out]
	In, Out int
}

func NewLinear(in, out int) *Linear {
	l := &Linear{In: in, Out: out}
	l.Weight = make([][]*Param, out)
	l.Bias = make([]*Param, out)
	for i := 0; i < out; i++ {
		l.Weight[i] = make([]*Param, in)
		for j := 0; j < in; j++ {
			// Xavier init
			scale := float32(math.Sqrt(2.0 / float64(in)))
			l.Weight[i][j] = NewParam(float32(rand.NormFloat64()) * scale)
		}
		l.Bias[i] = NewParam(0)
	}
	return l
}

func (l *Linear) Forward(x []*Value) []*Value {
	out := make([]*Value, l.Out)
	for i := 0; i < l.Out; i++ {
		sum := NewValue(0)
		for j := 0; j < l.In; j++ {
			sum = sum.Add(x[j].Mul(l.Weight[i][j].Value))
		}
		if l.Bias[i] != nil {
			sum = sum.Add(l.Bias[i].Value)
		}
		out[i] = sum
	}
	return out
}

func (l *Linear) Params() []*Param {
	params := make([]*Param, 0, l.In*l.Out+l.Out)
	for i := 0; i < l.Out; i++ {
		params = append(params, l.Weight[i]...)
		params = append(params, l.Bias[i])
	}
	return params
}

func (l *Linear) ZeroGrad() {
	for _, p := range l.Params() {
		p.ZeroGrad()
	}
}

// ============================================================
// GRU Cell
// ============================================================

type GRUCell struct {
	// Update gate: z = σ(x*W_z + h*U_z + b_z)
	Wz *Linear // input -> update
	Uz *Linear // hidden -> update
	// Reset gate: r = σ(x*W_r + h*U_r + b_r)
	Wr *Linear
	Ur *Linear
	// Candidate: h̃ = tanh(x*W_h + (r⊙h)*U_h + b_h)
	Wh         *Linear
	Uh         *Linear
	HiddenSize int
}

func NewGRUCell(inputSize, hiddenSize int) *GRUCell {
	return &GRUCell{
		Wz:         NewLinear(inputSize, hiddenSize),
		Uz:         NewLinear(hiddenSize, hiddenSize),
		Wr:         NewLinear(inputSize, hiddenSize),
		Ur:         NewLinear(hiddenSize, hiddenSize),
		Wh:         NewLinear(inputSize, hiddenSize),
		Uh:         NewLinear(hiddenSize, hiddenSize),
		HiddenSize: hiddenSize,
	}
}

func (g *GRUCell) Forward(x []*Value, h []*Value) []*Value {
	// Update gate
	z := make([]*Value, g.HiddenSize)
	for i := 0; i < g.HiddenSize; i++ {
		xz := g.Wz.Forward(x)[i]
		hz := g.Uz.Forward(h)[i]
		z[i] = xz.Add(hz).Sigmoid()
	}

	// Reset gate
	r := make([]*Value, g.HiddenSize)
	for i := 0; i < g.HiddenSize; i++ {
		xr := g.Wr.Forward(x)[i]
		hr := g.Ur.Forward(h)[i]
		r[i] = xr.Add(hr).Sigmoid()
	}

	// Candidate
	hTilde := make([]*Value, g.HiddenSize)
	for i := 0; i < g.HiddenSize; i++ {
		xh := g.Wh.Forward(x)[i]
		rh := make([]*Value, g.HiddenSize)
		for j := 0; j < g.HiddenSize; j++ {
			rh[j] = r[j].Mul(h[j])
		}
		uh := g.Uh.Forward(rh)[i]
		hTilde[i] = xh.Add(uh).Tanh()
	}

	// New hidden: h = (1-z)⊙h + z⊙h̃
	hNew := make([]*Value, g.HiddenSize)
	for i := 0; i < g.HiddenSize; i++ {
		oneMinusZ := NewValue(1).Add(z[i].Mul(NewValue(-1)))
		hNew[i] = oneMinusZ.Mul(h[i]).Add(z[i].Mul(hTilde[i]))
	}
	return hNew
}

func (g *GRUCell) Params() []*Param {
	params := []*Param{}
	params = append(params, g.Wz.Params()...)
	params = append(params, g.Uz.Params()...)
	params = append(params, g.Wr.Params()...)
	params = append(params, g.Ur.Params()...)
	params = append(params, g.Wh.Params()...)
	params = append(params, g.Uh.Params()...)
	return params
}

func (g *GRUCell) ZeroGrad() {
	for _, l := range []*Linear{g.Wz, g.Uz, g.Wr, g.Ur, g.Wh, g.Uh} {
		l.ZeroGrad()
	}
}

// ============================================================
// GRU Layer (sequence)
// ============================================================

type GRU struct {
	Cell   *GRUCell
	Hidden []*Value
}

func NewGRU(inputSize, hiddenSize int) *GRU {
	cell := NewGRUCell(inputSize, hiddenSize)
	h := make([]*Value, hiddenSize)
	for i := 0; i < hiddenSize; i++ {
		h[i] = NewValue(0)
	}
	return &GRU{Cell: cell, Hidden: h}
}

func (g *GRU) Forward(x []*Value) []*Value {
	g.Hidden = g.Cell.Forward(x, g.Hidden)
	return g.Hidden
}

func (g *GRU) Reset() {
	for i := range g.Hidden {
		g.Hidden[i].Data = 0
	}
}

func (g *GRU) Params() []*Param {
	return g.Cell.Params()
}

func (g *GRU) ZeroGrad() {
	g.Cell.ZeroGrad()
}

// ============================================================
// Sequential Container
// ============================================================

type Module interface {
	Forward(x []*Value) []*Value
	Params() []*Param
	ZeroGrad()
}

type Sequential struct {
	Layers []Module
}

func (s *Sequential) Forward(x []*Value) []*Value {
	out := x
	for _, l := range s.Layers {
		out = l.Forward(out)
	}
	return out
}

func (s *Sequential) Params() []*Param {
	var params []*Param
	for _, l := range s.Layers {
		params = append(params, l.Params()...)
	}
	return params
}

func (s *Sequential) ZeroGrad() {
	for _, l := range s.Layers {
		l.ZeroGrad()
	}
}

func NewSequential(layers ...Module) *Sequential {
	return &Sequential{Layers: layers}
}

// ============================================================
// Activations as Modules
// ============================================================

type Activation struct {
	Fn func(*Value) *Value
}

func (a *Activation) Forward(x []*Value) []*Value {
	out := make([]*Value, len(x))
	for i, x := range x {
		out[i] = a.Fn(x)
	}
	return out
}

func (a *Activation) Params() []*Param { return nil }
func (a *Activation) ZeroGrad()        {}

func Tanh() *Activation    { return &Activation{Fn: func(v *Value) *Value { return v.Tanh() }} }
func Sigmoid() *Activation { return &Activation{Fn: func(v *Value) *Value { return v.Sigmoid() }} }
func ReLU() *Activation {
	return &Activation{Fn: func(v *Value) *Value {
		out := NewValue(0)
		if v.Data > 0 {
			out.Data = v.Data
		}
		return out
	}}
}

// ============================================================
// Optimizer: SGD with momentum
// ============================================================

type SGD struct {
	Lr       float32
	Momentum float32
	Velocity map[*Value]float32
}

func NewSGD(lr, momentum float32) *SGD {
	return &SGD{Lr: lr, Momentum: momentum, Velocity: make(map[*Value]float32)}
}

func (o *SGD) Step(params []*Param) {
	for _, p := range params {
		v := p.Value
		vel := o.Velocity[v]
		vel = o.Momentum*vel + o.Lr*v.Grad
		o.Velocity[v] = vel
		v.Data -= vel
	}
}

func (o *SGD) ZeroGrad(params []*Param) {
	for _, p := range params {
		p.ZeroGrad()
	}
}

// ============================================================
// Loss Functions
// ============================================================

func MSELoss(pred, target []*Value) *Value {
	if len(pred) != len(target) {
		panic("MSELoss: length mismatch")
	}
	sum := NewValue(0)
	for i := range pred {
		diff := pred[i].Add(target[i].Mul(NewValue(-1)))
		sq := diff.Mul(diff)
		sum = sum.Add(sq)
	}
	return sum.Mul(NewValue(1.0 / float32(len(pred))))
}

func CrossEntropyLoss(pred, target []*Value) *Value {
	if len(pred) != len(target) {
		panic("CrossEntropyLoss: length mismatch")
	}
	sum := NewValue(0)
	for i := range pred {
		// -target * log(pred) - (1-target)*log(1-pred)
		p := pred[i].Data
		if p < 1e-7 {
			p = 1e-7
		}
		if p > 1-1e-7 {
			p = 1 - 1e-7
		}
		t := target[i].Data
		loss := -t*float32(math.Log(float64(p))) - (1-t)*float32(math.Log(float64(1-p)))
		sum = sum.Add(NewValue(loss))
	}
	return sum.Mul(NewValue(1.0 / float32(len(pred))))
}
