package platform

import (
	"fmt"
	"runtime"
)

type Registers struct {
	IP uintptr
	SP uintptr
	AX uintptr
	BX uintptr
	CX uintptr
	DX uintptr
}

type Context interface {
	Regs() Registers
	SetRegs(Registers)
	InstructionPointer() uintptr
	PlatformName() string
}

type genericContext struct {
	regs         Registers
	platformName string
}

func (c *genericContext) Regs() Registers             { return c.regs }
func (c *genericContext) SetRegs(r Registers)         { c.regs = r }
func (c *genericContext) InstructionPointer() uintptr { return c.regs.IP }
func (c *genericContext) PlatformName() string        { return c.platformName }

type PlatformDriver interface {
	NewContext() (Context, error)
	DetectCapabilities() []string
	KernelImplantType() string
	Release()
}

type ApexPlatform struct {
	targetOS   string
	targetArch string
}

func NewApexPlatform() PlatformDriver {
	return &ApexPlatform{
		targetOS:   runtime.GOOS,
		targetArch: runtime.GOARCH,
	}
}

func (p *ApexPlatform) NewContext() (Context, error) {
	name := fmt.Sprintf("%s-%s-apex-omni-hyperdriver-v8", p.targetOS, p.targetArch)
	return &genericContext{
		regs: Registers{
			IP: 0x400080,
			SP: 0x7fff7ffff000,
			AX: 0, BX: 0, CX: 0, DX: 0,
		},
		platformName: name,
	}, nil
}

func (p *ApexPlatform) DetectCapabilities() []string {
	switch p.targetOS {
	case "linux":
		return []string{"SYSTRAP_INTERCEPT", "SECCOMP_BPF", "KVM_ACCELERATION", "EPIGENETIC_MEMORY", "HIVE_MESH", "AUTONOMOUS_JIT", "QUIC_MESH", "RAFT_CONSENSUS"}
	case "windows":
		return []string{"NT_KERNEL_CALLBACK", "DEVICE_IO_CONTROL", "OB_REGISTER_CALLBACKS", "AMSI_BYPASS_SHIELD", "EPIGENETIC_MEMORY", "LIVE_MIGRATION", "QUIC_MESH"}
	case "darwin":
		return []string{"ENDPOINT_SECURITY", "TASK_FOR_PID_HOOK", "MACF_POLICY", "EPIGENETIC_MEMORY", "HIVE_MESH", "QUIC_MESH"}
	case "freebsd":
		return []string{"JAIL_ISOLATION", "CAP_ENTER_MODE", "NETGRAPH_HOOK", "EPIGENETIC_MEMORY", "HIVE_MESH", "QUIC_MESH"}
	case "openbsd":
		return []string{"PLEDGE_ENFORCE", "UNVEIL_RESTRICT", "W^X_PROTECTION", "EPIGENETIC_MEMORY", "HIVE_MESH", "QUIC_MESH"}
	default:
		return []string{"GENERIC_USERSPACE_VIRT", "EPIGENETIC_PERSISTENCE", "AUTONOMOUS_JIT", "QUIC_MESH"}
	}
}

func (p *ApexPlatform) KernelImplantType() string {
	switch p.targetOS {
	case "linux":
		return "LINUX_USERSPACE_PTRACE_SYSTRAP_APEX_V8"
	case "windows":
		return "WINDOWS_NT_DRIVER_IOCTL_OB_CALLBACK_IMPLANT_V8"
	case "darwin":
		return "MACOS_ENDPOINT_SECURITY_MACF_CLIENT_V8"
	case "freebsd":
		return "FREEBSD_JAIL_CAP_ENTER_IMMUNE_WRAPPER_V8"
	case "openbsd":
		return "OPENBSD_PLEDGE_UNVEIL_FORTIFIED_V8"
	default:
		return "GENERIC_APEX_OMNI_IMPLANT_V8"
	}
}

func (p *ApexPlatform) Release() {}
