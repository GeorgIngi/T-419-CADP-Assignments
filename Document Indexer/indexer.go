package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// DocumentIDs represents a set of document identifiers (file paths).
type DocumentIDs map[string]struct{}

// SearchEngine is a stateful index over documents.
//
// Concurrency model: SearchEngine is NOT thread-safe by itself. This program
// avoids data races by ensuring all mutations happen in a single reducer
// goroutine (the main goroutine while building the index).
type SearchEngine struct {
	// index maps a term to the set of documents containing that term.
	index map[string]DocumentIDs
	// counts maps document -> (term -> occurrences).
	counts map[string]map[string]int
	// totals maps document -> total number of terms in that document.
	totals map[string]int
	// docs contains all known documents.
	docs DocumentIDs
}

func NewSearchEngine() *SearchEngine {
	return &SearchEngine{
		index:  make(map[string]DocumentIDs),
		counts: make(map[string]map[string]int),
		totals: make(map[string]int),
		docs:   make(DocumentIDs),
	}
}

// AddDocument adds (or replaces) a document in the engine.
//
// indexer.go: AddDocument is only called by the reducer goroutine.
func (se *SearchEngine) AddDocument(docID string, freq map[string]int, totalTerms int) {
	se.docs[docID] = struct{}{}
	se.counts[docID] = freq
	se.totals[docID] = totalTerms

	for term := range freq {
		set, ok := se.index[term]
		if !ok {
			set = make(DocumentIDs)
			se.index[term] = set
		}
		set[docID] = struct{}{}
	}
}

// IndexLookup returns the set of documents containing term.
// makes sure not to corrupt the index by returning a slice
func (se *SearchEngine) IndexLookup(term string) []string {
	set, ok := se.index[term]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for doc := range set {
		out = append(out, doc)
	}
	return out
}

// TermFrequency computes tf(t,d) = n_{t,d} / sum_{t' in d} n_{t',d}.
func (se *SearchEngine) TermFrequency(term, docID string) float64 {
	total := se.totals[docID]
	if total <= 0 {
		return 0
	}
	cnt := 0
	if m, ok := se.counts[docID]; ok {
		cnt = m[term]
	}
	return float64(cnt) / float64(total)
}

// InverseDocumentFrequency computes idf(t,D) = log(N / n_t).
func (se *SearchEngine) InverseDocumentFrequency(term string) float64 {
	N := len(se.docs)
	if N == 0 {
		return 0
	}
	nt := len(se.index[term])
	if nt == 0 {
		return 0
	}

	// Add one in the denominator to closer match Marcel's relevance score
	return math.Log(float64(N) / (float64(nt) + 1))
}

// TfIdf computes tf-idf(t,d,D) = tf(t,d) * idf(t,D).
func (se *SearchEngine) TfIdf(term, docID string) float64 {
	return se.TermFrequency(term, docID) * se.InverseDocumentFrequency(term)
}

type relevanceResult struct {
	doc   string
	score float64
}

// RelevanceLookup returns all documents containing term, sorted from highest
// tf-idf to lowest, with doc path as a deterministic tie-breaker.
func (se *SearchEngine) RelevanceLookup(term string) []relevanceResult {
	docs := se.IndexLookup(term)
	out := make([]relevanceResult, 0, len(docs))
	for _, docID := range docs {
		out = append(out, relevanceResult{
			doc:   docID,
			score: se.TfIdf(term, docID),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score == out[j].score {
			return out[i].doc < out[j].doc
		}
		return out[i].score > out[j].score
	})
	return out
}

// mapResult is the output of the map phase for a single file.
type mapResult struct {
	path  string
	freq  map[string]int
	total int
	err   error
}

// tokenizeRegex extracts "terms" from text. It keeps internal apostrophes,
// e.g. o'er or o’er becomes one term. Unicode apostrophe (’) is supported.
var tokenizeRegex = regexp.MustCompile(`[\p{L}\p{N}]+(?:['’][\p{L}\p{N}]+)*`)

// mapFile reads a file and returns its word-frequency map and total term count.
func mapFile(path string) (map[string]int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	freq := make(map[string]int)
	total := 0

	scanner := bufio.NewScanner(f)
	// Some Shakespeare lines can be long; increase buffer to avoid token too long.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		terms := tokenizeRegex.FindAllString(line, -1)
		for _, t := range terms {
			term := strings.ToLower(t)
			freq[term]++
			total++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return freq, total, nil
}

// walkFiles recursively lists all regular files under root.
func walkFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// worker reads file paths from jobs, maps each file, and sends results.
func worker(jobs <-chan string, results chan<- mapResult) {
	for path := range jobs {
		freq, total, err := mapFile(path)
		results <- mapResult{path: path, freq: freq, total: total, err: err}
	}
}

func chooseWorkerCount(numFiles int) int {
	// Limit file descriptors by limiting concurrent open files.
	// Pick something "high enough" for parallelism but bounded.
	// - runtime.NumCPU()*4 keep CPU busy even with IO stalls.
	// - cap at 32 to avoid exhausting file descriptors on typical systems.
	workers := runtime.NumCPU() * 4
	workers = max(workers, 4)
	workers = min(workers, 32)
	workers = min(workers, max(1, numFiles))
	return workers
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: go run indexer.go ${DIRECTORY}")
	}
	root := os.Args[1]
	st, err := os.Stat(root)
	if err != nil {
		fatalf("error: %v", err)
	}
	if !st.IsDir() {
		fatalf("error: %s is not a directory", root)
	}

	paths, err := walkFiles(root)
	if err != nil {
		fatalf("error while scanning directory: %v", err)
	}

	se := NewSearchEngine()
	if len(paths) > 0 {
		workers := chooseWorkerCount(len(paths))
		jobs := make(chan string)
		results := make(chan mapResult, workers)

		for i := 0; i < workers; i++ {
			go worker(jobs, results)
		}

		// Feed jobs.
		go func() {
			for _, p := range paths {
				jobs <- p
			}
			close(jobs)
		}()

		// Reduce results (single goroutine: the main goroutine).
		for range paths {
			res := <-results
			if res.err != nil {
				// Handle worker error: report and skip document.
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", res.path, res.err)
				continue
			}
			se.AddDocument(res.path, res.freq, res.total)
		}
	}

	// Read terms from stdin and answer queries.
	if err := runQueries(os.Stdin, os.Stdout, se); err != nil {
		if !errors.Is(err, io.EOF) {
			fatalf("error: %v", err)
		}
	}
}

func runQueries(r io.Reader, w io.Writer, se *SearchEngine) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		term := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if term == "" {
			continue
		}

		results := se.RelevanceLookup(term)
		fmt.Fprintf(w, "== %s (%d)\n", term, len(results))
		for _, rr := range results {
			fmt.Fprintf(w, "%s,%.6f\n", rr.doc, rr.score)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
