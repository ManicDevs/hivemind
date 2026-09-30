package language

import "testing"

func TestLocalModelScorePrefersDistilledCounsel(t *testing.T) {
	// hivemind-train mints models named e.g. "hivemind-counsel:latest";
	// the mesh must auto-prefer the trained weights over every base model
	// the daemon also serves — including qwen3, which normally wins on
	// reasoning quality.
	cases := map[string]int{
		"hivemind-counsel:latest": 60,
		"hivemind-counsel:v2":     60,
		"llama2:7b":               20,
		"llama3:8b":               30,
		"qwen3:14b":               50,
		"nomic-embed-text:v1.5":   -1,
	}
	for name, want := range cases {
		if got := localModelScore(name); got != want {
			t.Errorf("localModelScore(%q) = %d, want %d", name, got, want)
		}
	}
	if localModelScore("hivemind-counsel:latest") <= localModelScore("qwen3:14b") {
		t.Error("distilled counsel must outrank qwen3 in the model race")
	}
}
