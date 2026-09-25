package main

type DependencyGraph struct {
	graph    map[string]map[string]bool
	roots    map[string]bool
	contains map[string]map[string]bool // tracks which functions contain/define other functions
}
