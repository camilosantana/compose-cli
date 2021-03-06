// +build local

/*
   Copyright 2020 Docker Compose CLI authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package local

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/compose-spec/compose-go/types"
	"golang.org/x/sync/errgroup"
)

type ServiceStatus int

const (
	ServiceStopped ServiceStatus = iota
	ServiceStarted
)

func inDependencyOrder(ctx context.Context, project *types.Project, fn func(context.Context, types.ServiceConfig) error) error {
	g := NewGraph(project.Services)
	if b, err := g.HasCycles(); b {
		return err
	}

	leaves := g.Leaves()

	eg, _ := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return run(ctx, g, eg, leaves, fn)
	})

	return eg.Wait()
}

// Note: this could be `graph.walk` or whatever
func run(ctx context.Context, graph *Graph, eg *errgroup.Group, nodes []*Vertex, fn func(context.Context, types.ServiceConfig) error) error {
	for _, node := range nodes {
		n := node
		// Don't start this service yet if all of its children have
		// not been started yet.
		if len(graph.FilterChildren(n.Service.Name, ServiceStopped)) != 0 {
			continue
		}

		eg.Go(func() error {
			err := fn(ctx, n.Service)
			if err != nil {
				return err
			}

			graph.UpdateStatus(n.Service.Name, ServiceStarted)

			return run(ctx, graph, eg, n.GetParents(), fn)
		})
	}

	return nil
}

type Graph struct {
	Vertices map[string]*Vertex
	lock     sync.RWMutex
}

type Vertex struct {
	Key      string
	Service  types.ServiceConfig
	Status   ServiceStatus
	Children map[string]*Vertex
	Parents  map[string]*Vertex
}

func (v *Vertex) GetParents() []*Vertex {
	var res []*Vertex
	for _, p := range v.Parents {
		res = append(res, p)
	}
	return res
}

func NewGraph(services types.Services) *Graph {
	graph := &Graph{
		lock:     sync.RWMutex{},
		Vertices: map[string]*Vertex{},
	}

	for _, s := range services {
		graph.AddVertex(s.Name, s)
	}

	for _, s := range services {
		for _, name := range s.GetDependencies() {
			graph.AddEdge(s.Name, name)
		}
	}

	return graph
}

// We then create a constructor function for the Vertex
func NewVertex(key string, service types.ServiceConfig) *Vertex {
	return &Vertex{
		Key:      key,
		Service:  service,
		Status:   ServiceStopped,
		Parents:  map[string]*Vertex{},
		Children: map[string]*Vertex{},
	}
}

func (g *Graph) AddVertex(key string, service types.ServiceConfig) {
	g.lock.Lock()
	defer g.lock.Unlock()

	v := NewVertex(key, service)
	g.Vertices[key] = v
}

func (g *Graph) AddEdge(source string, destination string) error {
	g.lock.Lock()
	defer g.lock.Unlock()

	sourceVertex := g.Vertices[source]
	destinationVertex := g.Vertices[destination]

	if sourceVertex == nil {
		return fmt.Errorf("could not find %s", source)
	}
	if destinationVertex == nil {
		return fmt.Errorf("could not find %s", destination)
	}

	// If they are already connected
	if _, ok := sourceVertex.Children[destination]; ok {
		return nil
	}

	sourceVertex.Children[destination] = destinationVertex
	destinationVertex.Parents[source] = sourceVertex

	return nil
}

func (g *Graph) Leaves() []*Vertex {
	g.lock.Lock()
	defer g.lock.Unlock()

	var res []*Vertex
	for _, v := range g.Vertices {
		if len(v.Children) == 0 {
			res = append(res, v)
		}
	}

	return res
}

func (g *Graph) UpdateStatus(key string, status ServiceStatus) {
	g.lock.Lock()
	defer g.lock.Unlock()
	g.Vertices[key].Status = status
}

func (g *Graph) FilterChildren(key string, status ServiceStatus) []*Vertex {
	g.lock.Lock()
	defer g.lock.Unlock()

	var res []*Vertex
	vertex := g.Vertices[key]

	for _, child := range vertex.Children {
		if child.Status == status {
			res = append(res, child)
		}
	}

	return res
}

func (g *Graph) HasCycles() (bool, error) {
	discovered := []string{}
	finished := []string{}

	for _, vertex := range g.Vertices {
		path := []string{
			vertex.Key,
		}
		if !contains(discovered, vertex.Key) && !contains(finished, vertex.Key) {
			var err error
			discovered, finished, err = g.visit(vertex.Key, path, discovered, finished)

			if err != nil {
				return true, err
			}
		}
	}

	return false, nil
}

func (g *Graph) visit(key string, path []string, discovered []string, finished []string) ([]string, []string, error) {
	discovered = append(discovered, key)

	for _, v := range g.Vertices[key].Children {
		path := append(path, v.Key)
		if contains(discovered, v.Key) {
			return nil, nil, fmt.Errorf("cycle found: %s", strings.Join(path, " -> "))
		}

		if !contains(finished, v.Key) {
			if _, _, err := g.visit(v.Key, path, discovered, finished); err != nil {
				return nil, nil, err
			}
		}
	}

	discovered = remove(discovered, key)
	finished = append(finished, key)
	return discovered, finished, nil
}

func remove(slice []string, item string) []string {
	var s []string
	for _, i := range slice {
		if i != item {
			s = append(s, i)
		}
	}
	return s
}
