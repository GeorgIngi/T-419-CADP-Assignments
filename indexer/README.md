# README

### How to run

Run the program with a directory to index:

  `go run indexer.go ${DIRECTORY}`

After indexing finishes, the program reads terms (one per line) from standard input and prints results to standard output:

  `printf "juliet\nromeo\n" | go run indexer.go shakespeare`

Output format:

  term (count)
  document,tfidf
  document,tfidf

Where `count` is the number of indexed documents that contain `term`.

### Files

* `indexer.go`  - Full implementation (directory traversal, concurrent indexing, tf-idf, and query loop).
* `AUTHORS`     - Group members.
* `agentlogs/`  - Logs of AI interactions.

### Interpretation notes

* Terms are extracted using a Unicode-aware regular expression that keeps internal apostrophes, so `o'er` / `o’er` becomes a single term (see `tokenizeRegex` in `indexer.go`, around lines 133-136).
* All terms are lowercased (case-insensitive indexing).

### Concurrency design (map-reduce)

The program follows a map-reduce structure:

* Map: `worker()` (indexer.go ~195-200) reads a file path, calls `mapFile()` (~139-171) which returns a per-document word-frequency map and a total term count.
* Reduce: `main()` receives `mapResult` values from `results` and updates the global `SearchEngine` by calling `AddDocument()` (~44-63).

Directory traversal (`walkFiles()`, ~173-192) happens once up front and produces the list of file paths.

### (a) How do you avoid data races?

All shared state is inside `SearchEngine` (`index`, `counts`, `totals`, `docs`). To avoid data races, we ensure that only the reducer mutates this shared state:

* Worker goroutines never write to `SearchEngine`. They only read files and send immutable results through the `results` channel (`worker()`, ~195-200).
* The reducer is the main goroutine during indexing and is the *only* goroutine that calls `se.AddDocument(...)` (`main()`, ~269-278).

Because there is a single writer to the shared maps, no mutexes are required and there are no concurrent map writes.

### (b) Why is your solution deadlock-free?

During indexing:

* `jobs` is closed exactly once by the feeder goroutine after all file paths are sent (`main()`, ~261-267). Workers range over `jobs`, so they terminate naturally when `jobs` is closed (`worker()`, ~195-200).
* The reducer performs exactly `len(paths)` receives from `results` (`main()`, ~270-278). Each path sent on `jobs` produces exactly one send on `results` (one `mapResult` per file), so the receive count matches the send count.
* There is no circular waiting: workers only block on `jobs` receives and `results` sends; the reducer only blocks on `results` receives.

Therefore, all goroutines either finish or can make progress until the reducer has received all results.

### (c) How do you handle errors in go-routines?

Errors are carried back to the reducer as part of the `mapResult` struct (`mapResult.err`, ~117-123). In `main()` (~270-276), if a worker reports an error (e.g., file cannot be opened/read), the reducer prints a warning to stderr and skips indexing that document.

This keeps the program robust: a single unreadable file does not crash the entire indexing run.

### How the number of goroutines is determined

We start one worker goroutine per "concurrent file".

`chooseWorkerCount()` (indexer.go ~216-226) chooses:

* `runtime.NumCPU()*4` as a baseline to overlap I/O with computation,
* at least 4 workers,
* at most 32 workers,
* and never more than the number of files.

This maximizes concurrency while preventing file descriptor exhaustion (each worker opens at most one file at a time).
