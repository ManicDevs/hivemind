package hivemind

// Observability hooks. The engine layer subscribes to these to keep a real
// telemetry surface without reaching into mind internals; when unset (nil)
// they cost one nil check per event and nothing else. A hook must never
// block the mind's speaking loop for a report.
var (
	// OnThought fires for every generated language thought: node, mind name,
	// and the prose itself complete and verbatim.
	OnThought func(node, name, text string)
	// OnReply fires for every composed cross-node answer: node, mind name,
	// the target mind's short identity, and the reply text verbatim.
	OnReply func(node, name, target, text string)
)

func fireThought(node, name, text string) {
	if OnThought != nil {
		OnThought(node, name, text)
	}
}

func fireReply(node, name, target, text string) {
	if OnReply != nil {
		OnReply(node, name, target, text)
	}
}
