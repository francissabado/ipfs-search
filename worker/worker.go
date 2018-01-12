package worker

import (
	"github.com/ipfs-search/ipfs-search/crawler"
	"github.com/ipfs-search/ipfs-search/indexer"
	"github.com/ipfs-search/ipfs-search/queue"
	"github.com/ipfs/go-ipfs-api"
	"time"
)

// Worker crawls hashes and files in parallel; it consumes hashes and files
// from respective queues and starts the configured number fo goroutines
// processing each.
type Worker struct {
	crawler      *crawler.Crawler
	config       *Config
	openChannels []*queue.TaskChannel // Channels to be closed later
}

func newCrawler(config *Config, addCh *queue.TaskChannel) (*crawler.Crawler, error) {
	// Create tasks queue's
	// As there's potential failure, execute this first to allow quick fail
	hq, err := queue.NewTaskQueue(addCh, "hashes")
	if err != nil {
		addCh.Close()
		return nil, err
	}

	fq, err := queue.NewTaskQueue(addCh, "files")
	if err != nil {
		addCh.Close()
		return nil, err
	}

	// Create and configure Ipfs shell
	sh := shell.NewShell(config.IpfsAPI)
	sh.SetTimeout(config.IpfsTimeout)

	// Create elasticsearch indexer
	id := indexer.NewIndexer(config.ElasticSearch)

	c := &crawler.Crawler{
		Config:    config.CrawlerConfig,
		Shell:     sh,
		Indexer:   id,
		FileQueue: fq,
		HashQueue: hq,
	}

	return c, nil
}

// New returns an initialized worker
func New(config *Config) (*Worker, error) {
	// These is the channel the crawler uses to add newly crawled hashes
	addCh, err := queue.NewChannel()
	if err != nil {
		return nil, err
	}

	c, err := newCrawler(config, addCh)
	if err != nil {
		return nil, err
	}

	return &Worker{
		crawler:      c,
		config:       config,
		openChannels: []*queue.TaskChannel{addCh},
	}, nil
}

// startHashWorkers starts hash workers
func (w *Worker) startHashWorkers(errc chan<- error) error {
	for i := 0; i < w.config.HashWorkers; i++ {
		// Now create queues and channel for workers
		ch, err := queue.NewChannel()
		if err != nil {
			return err
		}
		w.openChannels = append(w.openChannels, ch)

		hq, err := queue.NewTaskQueue(ch, "hashes")
		if err != nil {
			return err
		}
		hq.StartConsumer(func(params interface{}) error {
			args := params.(*crawler.Args)

			return w.crawler.CrawlHash(
				args.Hash,
				args.Name,
				args.ParentHash,
				args.ParentName,
			)
		}, &crawler.Args{}, errc)

		// Start workers timeout/hash time apart
		time.Sleep(w.config.HashWait)
	}

	return nil
}

// startFileWorkers starts file workers
func (w *Worker) startFileWorkers(errc chan<- error) error {
	for i := 0; i < w.config.FileWorkers; i++ {
		ch, err := queue.NewChannel()
		if err != nil {
			return err
		}
		w.openChannels = append(w.openChannels, ch)

		fq, err := queue.NewTaskQueue(ch, "files")
		if err != nil {
			return err
		}

		fq.StartConsumer(func(params interface{}) error {
			args := params.(*crawler.Args)

			return w.crawler.CrawlFile(
				args.Hash,
				args.Name,
				args.ParentHash,
				args.ParentName,
				args.Size,
			)
		}, &crawler.Args{}, errc)

		// Start workers timeout/hash time apart
		time.Sleep(w.config.FileWait)
	}

	return nil
}

// Start initiates crawling of the worker
func (w *Worker) Start(errc chan<- error) error {
	err := w.startHashWorkers(errc)
	if err != nil {
		w.Close()
		return err
	}
	err = w.startFileWorkers(errc)
	if err != nil {
		w.Close()
		return err
	}

	return nil
}

// Close destroy closes worker channels and frees resources
func (w *Worker) Close() error {
	for _, channel := range w.openChannels {
		err := channel.Close()
		if err != nil {
			return err
		}
	}

	return nil
}