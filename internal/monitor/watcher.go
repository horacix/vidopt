package monitor

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/horacix/vidopt/internal/config"
)

// FileWatcher monitors the library directory recursively with write-settle debouncing.
type FileWatcher struct {
	cfg        *config.Config
	fsw        *fsnotify.Watcher
	eventsOut  chan<- string
	stopChan   chan struct{}
	settleMap  map[string]*fileSettleState
	settleLock sync.Mutex
}

type fileSettleState struct {
	lastSize     int64
	lastActivity time.Time
}

// NewFileWatcher initializes a recursive directory watcher.
func NewFileWatcher(cfg *config.Config, eventsOut chan<- string) (*FileWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	fw := &FileWatcher{
		cfg:       cfg,
		fsw:       fsw,
		eventsOut: eventsOut,
		stopChan:  make(chan struct{}),
		settleMap: make(map[string]*fileSettleState),
	}

	return fw, nil
}

// Start begins monitoring the library recursively and checking for write stability.
func (fw *FileWatcher) Start() error {
	// Add root and all subdirectories to fsnotify
	err := filepath.WalkDir(fw.cfg.LibraryPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if fw.cfg.IsExcluded(path) {
				return filepath.SkipDir
			}
			if err := fw.fsw.Add(path); err != nil {
				log.Printf("[watcher] warning: could not watch directory %q: %v", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk library path %q: %w", fw.cfg.LibraryPath, err)
	}

	// Goroutine for fsnotify events
	go fw.eventLoop()

	// Goroutine for debouncing / write-settle detection
	go fw.debounceLoop()

	return nil
}

// Stop cleanly terminates the watcher.
func (fw *FileWatcher) Stop() {
	close(fw.stopChan)
	_ = fw.fsw.Close()
}

func (fw *FileWatcher) eventLoop() {
	for {
		select {
		case <-fw.stopChan:
			return
		case event, ok := <-fw.fsw.Events:
			if !ok {
				return
			}

			// If a new directory was created, add it and any nested subdirectories to watch list
			if event.Has(fsnotify.Create) {
				if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
					_ = filepath.WalkDir(event.Name, func(p string, d fs.DirEntry, err error) error {
						if err != nil {
							return nil
						}
						if d.IsDir() {
							if fw.cfg.IsExcluded(p) {
								return filepath.SkipDir
							}
							_ = fw.fsw.Add(p)
						}
						return nil
					})
					continue
				}
			}

			// If it's a video file event (Create or Write)
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				if fw.cfg.IsVideoFile(event.Name) && !fw.cfg.IsExcluded(event.Name) {
					fw.registerActivity(event.Name)
				}
			}

		case err, ok := <-fw.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("[watcher] fsnotify error: %v", err)
		}
	}
}

func (fw *FileWatcher) registerActivity(path string) {
	fw.settleLock.Lock()
	defer fw.settleLock.Unlock()

	stat, err := os.Stat(path)
	if err != nil {
		return
	}

	fw.settleMap[path] = &fileSettleState{
		lastSize:     stat.Size(),
		lastActivity: time.Now(),
	}
}

func (fw *FileWatcher) debounceLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	debounceDuration := time.Duration(fw.cfg.DebounceSeconds) * time.Second
	if debounceDuration <= 0 {
		debounceDuration = 10 * time.Second
	}

	for {
		select {
		case <-fw.stopChan:
			return
		case <-ticker.C:
			fw.checkSettledFiles(debounceDuration)
		}
	}
}

func (fw *FileWatcher) checkSettledFiles(debounceDuration time.Duration) {
	fw.settleLock.Lock()
	defer fw.settleLock.Unlock()

	now := time.Now()
	for path, state := range fw.settleMap {
		stat, err := os.Stat(path)
		if err != nil {
			delete(fw.settleMap, path)
			continue
		}

		currentSize := stat.Size()
		// If size changed, update activity timestamp
		if currentSize != state.lastSize {
			state.lastSize = currentSize
			state.lastActivity = now
			continue
		}

		// If size hasn't changed and settle timeout elapsed
		if now.Sub(state.lastActivity) >= debounceDuration {
			// Check if file is readable
			f, err := os.Open(path)
			if err == nil {
				_ = f.Close()
				delete(fw.settleMap, path)
				log.Printf("[watcher] file settled and ready: %s", path)
				select {
				case fw.eventsOut <- path:
				default:
					log.Printf("[watcher] warning: work queue full, dropping event for %s", path)
				}
			}
		}
	}
}
