package dagbee

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// DAGConfig is the top-level YAML configuration structure.
type DAGConfig struct {
	DAG struct {
		Name           string       `yaml:"name"`
		MaxConcurrency int          `yaml:"max_concurrency"`
		Timeout        string       `yaml:"timeout"`
		Nodes          []NodeConfig `yaml:"nodes"`
	} `yaml:"dag"`
}

// NodeConfig represents a single node in the YAML configuration.
type NodeConfig struct {
	Name      string      `yaml:"name"`
	Timeout   string      `yaml:"timeout"`
	Retry     RetryConfig `yaml:"retry"`
	Critical  *bool       `yaml:"critical"` // pointer to distinguish unset from false
	Priority  int         `yaml:"priority"`
	DependsOn []string    `yaml:"depends_on"`
}

// RetryConfig describes retry behaviour for a node.
type RetryConfig struct {
	Count    int    `yaml:"count"`
	Interval string `yaml:"interval"`
	Strategy string `yaml:"strategy"` // "fixed" (default) or "exponential"
}

// LoadDAGFromYAML reads a YAML file at path and builds a DAG.
// The registry maps each node name to its Go execution function.
// YAML controls the topology and configuration; Go code supplies the logic.
func LoadDAGFromYAML(path string, registry map[string]NodeFunc) (*DAG, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dagbee: failed to read config file: %w", err)
	}
	return LoadDAGFromYAMLBytes(data, registry)
}

// LoadDAGFromYAMLBytes builds a DAG from raw YAML bytes.
func LoadDAGFromYAMLBytes(data []byte, registry map[string]NodeFunc) (*DAG, error) {
	var cfg DAGConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("dagbee: failed to parse YAML config: %w", err)
	}

	var dagOpts []DAGOption
	if cfg.DAG.MaxConcurrency > 0 {
		dagOpts = append(dagOpts, WithMaxConcurrency(cfg.DAG.MaxConcurrency))
	}
	if cfg.DAG.Timeout != "" {
		timeout, err := time.ParseDuration(cfg.DAG.Timeout)
		if err != nil {
			return nil, fmt.Errorf("dagbee: invalid DAG timeout %q: %w", cfg.DAG.Timeout, err)
		}
		dagOpts = append(dagOpts, WithTimeout(timeout))
	}

	d := NewDAG(cfg.DAG.Name, dagOpts...)

	for _, nc := range cfg.DAG.Nodes {
		fn, ok := registry[nc.Name]
		if !ok {
			return nil, fmt.Errorf("dagbee: no function registered for node %q", nc.Name)
		}

		var nodeOpts []NodeOption

		if nc.Timeout != "" {
			timeout, err := time.ParseDuration(nc.Timeout)
			if err != nil {
				return nil, fmt.Errorf("dagbee: invalid timeout for node %q: %w", nc.Name, err)
			}
			nodeOpts = append(nodeOpts, NodeWithTimeout(timeout))
		}

		if nc.Retry.Count > 0 {
			interval := time.Duration(0)
			if nc.Retry.Interval != "" {
				var err error
				interval, err = time.ParseDuration(nc.Retry.Interval)
				if err != nil {
					return nil, fmt.Errorf("dagbee: invalid retry interval for node %q: %w", nc.Name, err)
				}
			}
			nodeOpts = append(nodeOpts, NodeWithRetry(nc.Retry.Count, interval))

			if nc.Retry.Strategy == "exponential" {
				nodeOpts = append(nodeOpts, NodeWithRetryStrategy(RetryExponential))
			}
		}

		if nc.Critical != nil {
			nodeOpts = append(nodeOpts, NodeWithCritical(*nc.Critical))
		}

		if nc.Priority != 0 {
			nodeOpts = append(nodeOpts, NodeWithPriority(nc.Priority))
		}

		if len(nc.DependsOn) > 0 {
			nodeOpts = append(nodeOpts, NodeWithDependsOn(nc.DependsOn...))
		}

		if err := d.AddNode(nc.Name, fn, nodeOpts...); err != nil {
			return nil, err
		}
	}

	return d, nil
}
