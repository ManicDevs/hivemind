// hivemind-train distills the lawbook journal into a standing local counsel
// model on the loopback Ollama daemon: build the supervised dataset, render
// the Modelfile with the whole corpus embedded, hand it to `ollama create`,
// then evaluate the freshly minted model against probe questions. Run with
// -apply to actually create the model; without it the artifacts are written
// and reported (dry run).
package main

import (
	"flag"
	"fmt"
	"os"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/train"
)

func main() {
	os.Exit(run())
}

func run() int {
	var opt train.Options
	apply := false
	var questions int
	fs := flag.NewFlagSet("hivemind-train", flag.ExitOnError)
	fs.StringVar(&opt.Journal, "journal", train.DefaultJournal, "lawbook journal to distill")
	fs.StringVar(&opt.BaseModel, "base", train.DefaultBaseModel, "foundation model (FROM)")
	fs.StringVar(&opt.ModelName, "name", train.DefaultModelName, "created model name")
	fs.StringVar(&opt.DatasetPath, "dataset", train.DefaultDatasetPath, "output supervised dataset (JSONL)")
	fs.StringVar(&opt.ModelfilePath, "modelfile", train.DefaultModelfilePath, "output Modelfile")
	fs.StringVar(&opt.Adapter, "adapter", "", "LoRA .safetensors to weave in as ADAPTER (optional)")
	fs.StringVar(&opt.OllamaBase, "ollama", train.DefaultOllama, "loopback Ollama daemon")
	fs.BoolVar(&apply, "apply", false, "actually create the model on the daemon (dry-run otherwise)")
	fs.IntVar(&questions, "questions", len(train.DefaultQuestions), "live evaluation probes to ask")
	_ = fs.Parse(os.Args[1:])

	clauses, err := train.LoadClauses(opt.Journal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  %v\n", err)
		return 1
	}
	if len(clauses) == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  journal %s holds no provisions. Seed the lawbook first:\n", opt.Journal)
		fmt.Fprintf(os.Stderr, "    hivemind seed -journal %s data/law.json\n", opt.Journal)
		return 2
	}

	recs := train.BuildDataset(clauses)
	if err := train.WriteDataset(opt.DatasetPath, recs); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  dataset: %v\n", err)
		return 1
	}
	modelfile := train.RenderModelfile(clauses, opt.BaseModel, opt.ModelName, opt.Adapter)
	if err := train.WriteModelfile(opt.ModelfilePath, modelfile); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  modelfile: %v\n", err)
		return 1
	}

	fmt.Printf("⚖️  [TRAIN] distilling %d provisions from %s\n", len(clauses), opt.Journal)
	fmt.Printf("   dataset     %s (%d supervised pairs)\n", opt.DatasetPath, len(recs))
	fmt.Printf("   modelfile   %s\n", opt.ModelfilePath)
	fmt.Printf("   target      %s ← FROM %s\n", opt.ModelName, opt.BaseModel)
	if opt.Adapter != "" {
		fmt.Printf("   adapter     %s\n", opt.Adapter)
	}

	if !apply {
		fmt.Println("   dry-run: artifacts written. Re-run with -apply to create the model.")
		return 0
	}

	fmt.Printf("   creating %s on %s …\n", opt.ModelName, opt.OllamaBase)
	if err := train.CreateModel(opt.OllamaBase, opt.ModelName, train.CreateOptions{
		From:       opt.BaseModel,
		System:     train.SystemBlock(clauses),
		Parameters: map[string]any{"temperature": 0.7, "top_p": 0.9, "num_ctx": 4096, "num_predict": 256},
		Adapter:    opt.Adapter,
		Legacy:     modelfile,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  %v\n", err)
		return 1
	}
	fmt.Printf("✅ [TRAIN] %s created (base %s, %d provisions embedded)\n", opt.ModelName, opt.BaseModel, len(clauses))

	q := train.DefaultQuestions
	if questions > 0 && questions < len(q) {
		q = q[:questions]
	}
	if len(q) > 0 {
		fmt.Printf("   evaluating %s live (%d probes) …\n", opt.ModelName, len(q))
		answers, err := train.EvaluateStreaming(opt.OllamaBase, opt.ModelName, q, func(a train.Answer) {
			fmt.Printf("\n   Q: %s\n", a.Question)
			fmt.Printf("   A: %s\n   (%s)\n", a.Answer, a.Duration.Round(100_000_000))
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  evaluation: %v\n", err)
			return 1
		}
		fmt.Printf("\n   round complete: %d probes\n", len(answers))
	}

	fmt.Printf("\n🎓 [TRAIN] point the mesh at the trained weights:\n")
	fmt.Printf("   HIVEMIND_OLLAMA_MODEL=%s hivemind engine -config node.json\n", opt.ModelName)
	return 0
}
