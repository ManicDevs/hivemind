//go:build release

package language

/*
#cgo linux LDFLAGS: -L${SRCDIR}/../../../../lib -lllama -lm -ldl -lpthread
#cgo darwin LDFLAGS: -L${SRCDIR}/../../../../lib -lllama -lm -lpthread

#include <stdlib.h>
#include <string.h>
#include "llama.h"

typedef struct {
    llama_model* model;
    llama_context* ctx;
    llama_sampler* sampler;
} LlamaInstance;

LlamaInstance* llama_init(const char* model_path, int ctx_size, int threads, int gpu_layers) {
    LlamaInstance* inst = malloc(sizeof(LlamaInstance));
    inst->model = llama_model_load_from_file(model_path, NULL);
    if (!inst->model) {
        free(inst);
        return NULL;
    }

    llama_context_params cparams = llama_context_default_params();
    cparams.n_ctx = ctx_size;
    cparams.n_threads = threads;
    cparams.n_gpu_layers = gpu_layers;

    inst->ctx = llama_init_from_model(inst->model, cparams);
    if (!inst->ctx) {
        llama_model_free(inst->model);
        free(inst);
        return NULL;
    }

    llama_sampler_chain_params sparams = llama_sampler_chain_default_params();
    sparams.no_perf = true;
    inst->sampler = llama_sampler_chain_init(sparams);
    llama_sampler_chain_add(inst->sampler, llama_sampler_init(LLAMA_SAMPLER_TOP_K, 40));
    llama_sampler_chain_add(inst->sampler, llama_sampler_init(LLAMA_SAMPLER_TOP_P, 0.9f));
    llama_sampler_chain_add(inst->sampler, llama_sampler_init(LLAMA_SAMPLER_TEMPERATURE, 0.7f));
    llama_sampler_chain_add(inst->sampler, llama_sampler_init(LLAMA_SAMPLER_DIST, 0));

    return inst;
}

void llama_free(LlamaInstance* inst) {
    if (inst) {
        if (inst->sampler) llama_sampler_free(inst->sampler);
        if (inst->ctx) llama_free(inst->ctx);
        if (inst->model) llama_model_free(inst->model);
        free(inst);
    }
}

char* llama_generate(LlamaInstance* inst, const char* prompt, int max_tokens, float temp, float top_p, int top_k) {
    int n_prompt = -llama_tokenize(inst->model, prompt, strlen(prompt), NULL, 0, true, true);
    int* tokens = malloc((n_prompt + 256) * sizeof(int));
    int n_tokens = llama_tokenize(inst->model, prompt, strlen(prompt), tokens, n_prompt + 256, true, true);

    if (n_tokens < 0) {
        free(tokens);
        return NULL;
    }

    llama_batch batch = llama_batch_get_one(tokens, n_tokens);
    llama_decode(inst->ctx, batch);

    char* output = malloc(4096);
    int out_len = 0;
    output[0] = '\0';

    for (int i = 0; i < max_tokens; i++) {
        llama_token id = llama_sampler_sample(inst->sampler, inst->ctx, -1);
        if (llama_token_is_eog(inst->model, id)) break;

        char buf[128];
        int n = llama_token_to_piece(inst->model, id, buf, sizeof(buf), 0, true);
        if (n > 0) {
            if (out_len + n < 4095) {
                memcpy(output + out_len, buf, n);
                out_len += n;
                output[out_len] = '\0';
            }
        }

        llama_batch batch2 = llama_batch_get_one(&id, 1);
        llama_decode(inst->ctx, batch2);
    }

    free(tokens);
    return output;
}
*/
import "C"
import (
	"context"
	"runtime"
	"unsafe"
)

// LlamaCpp provides real LLM inference via llama.cpp CGO bindings.
// Build with: go build -tags release -ldflags="-L/path/to/llama.cpp/lib -lllama"
type LlamaCpp struct {
	instance *C.LlamaInstance
	config   ModelConfig
}

func NewLlamaCpp(config ModelConfig) (*LlamaCpp, error) {
	if config.ModelPath == "" {
		return nil, ErrNoModelPath
	}

	cPath := C.CString(config.ModelPath)
	defer C.free(unsafe.Pointer(cPath))

	inst := C.llama_init(
		cPath,
		C.int(config.ContextSize),
		C.int(config.Threads),
		C.int(config.GPULayers),
	)
	if inst == nil {
		return nil, ErrModelLoadFailed
	}

	return &LlamaCpp{
		instance: inst,
		config:   config,
	}, nil
}

func (l *LlamaCpp) Generate(ctx context.Context, prompt string) (string, error) {
	cPrompt := C.CString(prompt)
	defer C.free(unsafe.Pointer(cPrompt))

	cOutput := C.llama_generate(
		l.instance,
		cPrompt,
		C.int(l.config.MaxTokens),
		C.float(l.config.Temperature),
		C.float(l.config.TopP),
		C.int(l.config.TopK),
	)
	if cOutput == nil {
		return "", ErrGenerationFailed
	}
	defer C.free(unsafe.Pointer(cOutput))

	return C.GoString(cOutput), nil
}

func (l *LlamaCpp) Name() string {
	return "llama.cpp (" + l.config.ModelPath + ")"
}

func (l *LlamaCpp) Close() error {
	if l.instance != nil {
		C.llama_free(l.instance)
		l.instance = nil
	}
	return nil
}

// Ensure cleanup on GC
func init() {
	runtime.SetFinalizer(&LlamaCpp{}, (*LlamaCpp).Close)
}
