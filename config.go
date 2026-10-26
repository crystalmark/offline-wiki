package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
)

// Book is one Kiwix library (a family of dated ZIM files).
type Book struct {
	Prefix  string  `json:"prefix"` // e.g. wikipedia_en_all_maxi
	Dir     string  `json:"dir"`    // directory on download.kiwix.org/zim/
	Label   string  `json:"label"`  // shown in the UI, e.g. "Wikipedia"
	Weight  float64 `json:"weight"` // relative importance when ranking results
	PerQ    int     `json:"per_query_results"`
	MaxDocs int     `json:"max_docs"` // cap on documents fetched from this book per question
	Enabled bool    `json:"enabled"`
	Local   bool    `json:"local,omitempty"` // made locally (e.g. with zimit), not downloaded from Kiwix
}

// Model is a GGUF language model the launcher can run with llamafile.
type Model struct {
	ID           string `json:"id"`
	Label        string `json:"label,omitempty"`    // shown in the page's model menu
	Thinking     bool   `json:"thinking,omitempty"` // reasons before answering (slower, more careful)
	NoThink      bool   `json:"no_think,omitempty"` // a hybrid model run with its thinking switched off
	File         string `json:"file"`
	URL          string `json:"url"`
	MinRAMGB     int    `json:"min_ram_gb"`
	Ctx          int    `json:"ctx"`           // context window passed to llamafile
	ContextChars int    `json:"context_chars"` // how much source text to put in the prompt
}

// RerankerModel is the small model that judges which passages actually
// answer the question.
type RerankerModel struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

type Config struct {
	Port               int           `json:"port"`
	KiwixPort          int           `json:"kiwix_port"`
	LLMPort            int           `json:"llm_port"`
	RerankPort         int           `json:"rerank_port"`
	QueryPort          int           `json:"query_port"`
	LAN                bool          `json:"lan"` // serve to other devices on the local network
	ZimMirror          string        `json:"zim_mirror"`
	ToolsMirror        string        `json:"tools_mirror"`
	LlamafileRepo      string        `json:"llamafile_repo"`
	UpdateIntervalDays int           `json:"update_interval_days"`
	Books              []Book        `json:"books"`
	Models             []Model       `json:"models"`
	Reranker           RerankerModel `json:"reranker"`
}

func defaultConfig() Config {
	return Config{
		Port:       8470,
		KiwixPort:  8471,
		LLMPort:    8472,
		RerankPort: 8473,
		QueryPort:  8474,
		Reranker: RerankerModel{
			File: "bge-reranker-v2-m3-Q8_0.gguf",
			URL:  "https://huggingface.co/gpustack/bge-reranker-v2-m3-GGUF/resolve/main/bge-reranker-v2-m3-Q8_0.gguf",
		},
		ZimMirror:          "https://download.kiwix.org/zim/",
		ToolsMirror:        "https://download.kiwix.org/release/kiwix-tools/",
		LlamafileRepo:      "mozilla-ai/llamafile",
		UpdateIntervalDays: 7,
		Books: []Book{
			{Prefix: "wikipedia_en_all_maxi", Dir: "wikipedia", Label: "Wikipedia", Weight: 1.0, PerQ: 6, MaxDocs: 8, Enabled: true},
			{Prefix: "wikipedia_en_medicine_maxi", Dir: "wikipedia", Label: "WikiMed", Weight: 0.9, PerQ: 3, MaxDocs: 3, Enabled: true},
			{Prefix: "medlineplus.gov_en_all", Dir: "zimit", Label: "MedlinePlus", Weight: 0.9, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "nhs.uk_en_medicines", Dir: "zimit", Label: "NHS medicines", Weight: 0.9, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "nhs.uk_en_health-a-z", Label: "NHS Health A–Z", Weight: 0.9, PerQ: 3, MaxDocs: 2, Enabled: true, Local: true},
			{Prefix: "gov.uk_en_highway-code", Label: "Highway Code", Weight: 0.9, PerQ: 3, MaxDocs: 2, Enabled: true, Local: true},
			{Prefix: "irp.fas.org_en_military-medicine", Dir: "zimit", Label: "Military medicine", Weight: 0.8, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "wwwnc.cdc.gov_en_all", Dir: "zimit", Label: "CDC travel health", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "armypubs_en_all", Dir: "zimit", Label: "Army field manuals", Weight: 0.8, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "ready.gov_en_all", Dir: "zimit", Label: "Ready.gov", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "appropedia_en_all_maxi", Dir: "other", Label: "Appropedia", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "cd3wdproject.org_en_all", Dir: "zimit", Label: "CD3WD", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "energypedia_en_all_maxi", Dir: "other", Label: "Energypedia", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "solar.lowtechmagazine.com_mul_all", Dir: "zimit", Label: "Low-tech Magazine", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "gardenology.org_en_all", Dir: "other", Label: "Gardenology", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "publicdomainrecipes.com_en_all", Dir: "zimit", Label: "Recipes", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "wikivoyage_en_all_maxi", Dir: "wikivoyage", Label: "Wikivoyage", Weight: 0.6, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "ifixit_en_all", Dir: "ifixit", Label: "iFixit", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "diy.stackexchange.com_en_all", Dir: "stack_exchange", Label: "DIY Q&A", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "electronics.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Electronics Q&A", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "mechanics.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Car mechanics Q&A", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "woodworking.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Woodworking Q&A", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "gardening.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Gardening Q&A", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "cooking.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Cooking Q&A", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "homebrew.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Homebrewing Q&A", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "outdoors.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Outdoors Q&A", Weight: 0.7, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "bicycles.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Bicycles Q&A", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "aviation.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Aviation Q&A", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "ham.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Amateur radio Q&A", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "sustainability.stackexchange.com_en_all", Dir: "stack_exchange", Label: "Sustainability Q&A", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "wikibooks_en_all_maxi", Dir: "wikibooks", Label: "Wikibooks", Weight: 0.6, PerQ: 3, MaxDocs: 2, Enabled: true},
			{Prefix: "wiktionary_en_all_nopic", Dir: "wiktionary", Label: "Wiktionary", Weight: 0.3, PerQ: 1, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_med", Dir: "libretexts", Label: "LibreTexts Medicine", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_bio", Dir: "libretexts", Label: "LibreTexts Biology", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_chem", Dir: "libretexts", Label: "LibreTexts Chemistry", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_phys", Dir: "libretexts", Label: "LibreTexts Physics", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_eng", Dir: "libretexts", Label: "LibreTexts Engineering", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_geo", Dir: "libretexts", Label: "LibreTexts Earth science", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_math", Dir: "libretexts", Label: "LibreTexts Mathematics", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_stats", Dir: "libretexts", Label: "LibreTexts Statistics", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_workforce", Dir: "libretexts", Label: "LibreTexts Trades", Weight: 0.6, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_human", Dir: "libretexts", Label: "LibreTexts Humanities", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_socialsci", Dir: "libretexts", Label: "LibreTexts Social sciences", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "libretexts.org_en_biz", Dir: "libretexts", Label: "LibreTexts Business", Weight: 0.5, PerQ: 2, MaxDocs: 1, Enabled: true},
			{Prefix: "gutenberg_en_all", Dir: "gutenberg", Label: "Project Gutenberg", Weight: 0.4, PerQ: 2, MaxDocs: 2, Enabled: false}, // 206 GB: needs a 1 TB drive
		},
		Models: []Model{
			{
				ID:           "large",
				Label:        "Standard",
				File:         "Qwen3-30B-A3B-Instruct-2507-Q4_K_M.gguf",
				URL:          "https://huggingface.co/unsloth/Qwen3-30B-A3B-Instruct-2507-GGUF/resolve/main/Qwen3-30B-A3B-Instruct-2507-Q4_K_M.gguf",
				MinRAMGB:     30,
				Ctx:          32768,
				ContextChars: 50000,
			},
			{
				ID:           "small",
				Label:        "Fast (small model)",
				File:         "Qwen3-4B-Instruct-2507-Q4_K_M.gguf",
				URL:          "https://huggingface.co/unsloth/Qwen3-4B-Instruct-2507-GGUF/resolve/main/Qwen3-4B-Instruct-2507-Q4_K_M.gguf",
				MinRAMGB:     0,
				Ctx:          16384,
				ContextChars: 22000,
			},
			{
				ID:           "medium",
				Label:        "Medium",
				NoThink:      true,
				File:         "Qwen3-14B-Q4_K_M.gguf",
				URL:          "https://huggingface.co/unsloth/Qwen3-14B-GGUF/resolve/main/Qwen3-14B-Q4_K_M.gguf",
				MinRAMGB:     16,
				Ctx:          16384,
				ContextChars: 30000,
			},
			{
				ID:           "thinking",
				Label:        "Thorough (thinks first, slower)",
				Thinking:     true,
				File:         "Qwen3-30B-A3B-Thinking-2507-Q4_K_M.gguf",
				URL:          "https://huggingface.co/unsloth/Qwen3-30B-A3B-Thinking-2507-GGUF/resolve/main/Qwen3-30B-A3B-Thinking-2507-Q4_K_M.gguf",
				MinRAMGB:     30,
				Ctx:          32768,
				ContextChars: 50000,
			},
		},
	}
}

func loadConfig(root string) (Config, error) {
	def := defaultConfig()
	b, err := os.ReadFile(filepath.Join(root, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	// Read into an empty Config: unmarshalling over the defaults would merge
	// lists item by item and mix one model's settings into another's.
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return def, fmt.Errorf("config.json: %w", err)
	}
	if cfg.Port == 0 {
		cfg.Port = def.Port
	}
	if cfg.KiwixPort == 0 {
		cfg.KiwixPort = def.KiwixPort
	}
	if cfg.LLMPort == 0 {
		cfg.LLMPort = def.LLMPort
	}
	if cfg.RerankPort == 0 {
		cfg.RerankPort = def.RerankPort
	}
	if cfg.QueryPort == 0 {
		cfg.QueryPort = def.QueryPort
	}
	if cfg.ZimMirror == "" {
		cfg.ZimMirror = def.ZimMirror
	}
	if cfg.ToolsMirror == "" {
		cfg.ToolsMirror = def.ToolsMirror
	}
	if cfg.LlamafileRepo == "" {
		cfg.LlamafileRepo = def.LlamafileRepo
	}
	if cfg.UpdateIntervalDays == 0 {
		cfg.UpdateIntervalDays = def.UpdateIntervalDays
	}
	if cfg.Reranker.File == "" {
		cfg.Reranker = def.Reranker
	}
	// Libraries and models added in newer versions of Offline Wiki join an
	// older config.json; to drop one, set "enabled": false rather than
	// deleting its entry.
	haveBook := map[string]bool{}
	for _, b := range cfg.Books {
		haveBook[b.Prefix] = true
	}
	for _, b := range def.Books {
		if !haveBook[b.Prefix] {
			cfg.Books = append(cfg.Books, b)
		}
	}
	haveModel := map[string]bool{}
	for i, m := range cfg.Models {
		haveModel[m.ID] = true
		for _, d := range def.Models {
			if d.ID == m.ID && m.Label == "" {
				cfg.Models[i].Label, cfg.Models[i].Thinking, cfg.Models[i].NoThink = d.Label, d.Thinking, d.NoThink
			}
		}
	}
	for _, m := range def.Models {
		if !haveModel[m.ID] {
			cfg.Models = append(cfg.Models, m)
		}
	}
	return cfg, nil
}

func writeConfig(root string, cfg Config) error {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(filepath.Join(root, "config.json"), append(b, '\n'), 0o644)
}

// findRoot works out the drive root: the executable lives in <root>/app/.
func findRoot(flagRoot string) (string, error) {
	if flagRoot != "" {
		return filepath.Abs(flagRoot)
	}
	exe, err := os.Executable()
	if err == nil {
		if r, err2 := filepath.EvalSymlinks(exe); err2 == nil {
			exe = r
		}
		dir := filepath.Dir(exe)
		if filepath.Base(dir) == "app" {
			return filepath.Dir(dir), nil
		}
	}
	return os.Getwd()
}

func platformKey() string { return runtime.GOOS + "-" + runtime.GOARCH }

// kiwixPlatforms maps our platform keys to Kiwix release names.
var kiwixPlatforms = map[string]string{
	"darwin-arm64":  "macos-arm64",
	"darwin-amd64":  "macos-x86_64",
	"linux-amd64":   "linux-x86_64",
	"linux-arm64":   "linux-aarch64",
	"windows-amd64": "win-x86_64",
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// LocalZim is a ZIM file present in the library folder.
type LocalZim struct {
	Book Book
	File string // base name
	Path string
	Date string
	Size int64
}

// Stem is the name kiwix-serve uses for the book (file name without .zim).
func (z LocalZim) Stem() string { return z.File[:len(z.File)-4] }

// localZims returns, for each enabled book, the newest ZIM present.
func localZims(root string, cfg Config) []LocalZim {
	var out []LocalZim
	lib := filepath.Join(root, "library")
	entries, _ := os.ReadDir(lib)
	for _, b := range cfg.Books {
		if !b.Enabled {
			continue
		}
		var cands []LocalZim
		re := regexp.MustCompile("^" + regexp.QuoteMeta(b.Prefix) + `_(\d{4}-\d{2}[a-z]?)\.zim$`)
		for _, e := range entries {
			m := re.FindStringSubmatch(e.Name())
			if m == nil || e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			cands = append(cands, LocalZim{Book: b, File: e.Name(), Path: filepath.Join(lib, e.Name()), Date: m[1], Size: info.Size()})
		}
		if len(cands) == 0 {
			continue
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].Date > cands[j].Date })
		out = append(out, cands[0])
	}
	return out
}
