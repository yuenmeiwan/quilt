package dsl

import (
	"fmt"
	"text/scanner"

	log "github.com/Sirupsen/logrus"
)

// A Dsl is an abstract representation of the policy language.
type Dsl struct {
	code string
	ctx  evalCtx
}

// A Container may be instantiated in the dsl and queried by users.
type Container struct {
	Image   string
	Command []string

	Placement
	atomImpl
}

// A Placement constraint restricts where containers may be instantiated.
type Placement struct {
	Exclusive map[[2]string]struct{}
}

// A Connection allows containers implementing the From label to speak to containers
// implementing the To label in ports in the range [MinPort, MaxPort]
type Connection struct {
	From    string
	To      string
	MinPort int
	MaxPort int
}

// A Machine specifies the type of VM that should be booted.
type Machine struct {
	Provider string
	Size     string
	CPU      Range
	RAM      Range
	SSHKeys  []string

	atomImpl
}

// A Range defines a range of acceptable values for a Machine attribute
type Range struct {
	Min float64
	Max float64
}

// Accepts returns true if `x` is within the range specified by `dslr` (include),
// or if no max is specified and `x` is larger than `dslr.min`.
func (dslr Range) Accepts(x float64) bool {
	return dslr.Min <= x && (dslr.Max == 0 || x <= dslr.Max)
}

// New parses and executes a dsl (in text form), and returns an abstract Dsl handle.
func New(sc scanner.Scanner, path []string) (Dsl, error) {
	parsed, err := parse(sc)
	if err != nil {
		return Dsl{}, err
	}

	parsed, err = resolveImports(parsed, path)
	if err != nil {
		return Dsl{}, err
	}

	_, ctx, err := eval(astRoot(parsed))
	if err != nil {
		return Dsl{}, err
	}

	return Dsl{astRoot(parsed).String(), ctx}, nil
}

// QueryContainers retreives all containers declared in dsl.
func (dsl Dsl) QueryContainers() []*Container {
	var containers []*Container
	for _, c := range *dsl.ctx.containers {
		var command []string
		for _, co := range c.command {
			command = append(command, string(co.(astString)))
		}
		containers = append(containers, &Container{
			Image:     string(c.image),
			Command:   command,
			Placement: c.Placement,
			atomImpl:  c.atomImpl,
		})
	}
	return containers
}

func parseKeys(rawKeys []key) []string {
	var keys []string
	for _, val := range rawKeys {
		key, ok := val.(key)
		if !ok {
			log.Warnf("%s: Requested []key, found %s", key, val)
			continue
		}

		parsedKeys, err := key.keys()
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
				"key":   key,
			}).Warning("Failed to retrieve key.")
			continue
		}

		keys = append(keys, parsedKeys...)
	}

	return keys
}

// QueryMachineSlice returns the machines associated with a label.
func (dsl Dsl) QueryMachineSlice(key string) []Machine {
	label, ok := dsl.ctx.labels[key]
	if !ok {
		log.Warnf("%s undefined", key)
		return nil
	}
	result := label.elems

	var machines []Machine
	for _, val := range result {
		machineAst, ok := val.(*astMachine)
		if !ok {
			log.Warnf("%s: Requested []machine, found %s", key, val)
			return nil
		}
		machines = append(machines, Machine{
			Provider: string(machineAst.provider),
			Size:     string(machineAst.size),
			RAM:      Range{Min: float64(machineAst.ram.min), Max: float64(machineAst.ram.max)},
			CPU:      Range{Min: float64(machineAst.cpu.min), Max: float64(machineAst.cpu.max)},
			SSHKeys:  parseKeys(machineAst.sshKeys),
			atomImpl: machineAst.atomImpl,
		})
	}

	return machines
}

// QueryConnections returns the connections declared in the dsl.
func (dsl Dsl) QueryConnections() []Connection {
	var connections []Connection
	for c := range dsl.ctx.connections {
		connections = append(connections, c)
	}
	return connections
}

// QueryFloat returns a float value defined in the dsl.
func (dsl Dsl) QueryFloat(key string) (float64, error) {
	result, ok := dsl.ctx.binds[astIdent(key)]
	if !ok {
		return 0, fmt.Errorf("%s undefined", key)
	}

	val, ok := result.(astFloat)
	if !ok {
		return 0, fmt.Errorf("%s: Requested float, found %s", key, val)
	}

	return float64(val), nil
}

// QueryInt returns an integer value defined in the dsl.
func (dsl Dsl) QueryInt(key string) int {
	result, ok := dsl.ctx.binds[astIdent(key)]
	if !ok {
		log.Warnf("%s undefined", key)
		return 0
	}

	val, ok := result.(astInt)
	if !ok {
		log.Warnf("%s: Requested int, found %s", key, val)
		return 0
	}

	return int(val)
}

// QueryString returns a string value defined in the dsl.
func (dsl Dsl) QueryString(key string) string {
	result, ok := dsl.ctx.binds[astIdent(key)]
	if !ok {
		log.Warnf("%s undefined", key)
		return ""
	}

	val, ok := result.(astString)
	if !ok {
		log.Warnf("%s: Requested string, found %s", key, val)
		return ""
	}

	return string(val)
}

// QueryStrSlice returns a string slice value defined in the dsl.
func (dsl Dsl) QueryStrSlice(key string) []string {
	result, ok := dsl.ctx.binds[astIdent(key)]
	if !ok {
		log.Warnf("%s undefined", key)
		return nil
	}

	val, ok := result.(astList)
	if !ok {
		log.Warnf("%s: Requested []string, found %s", key, val)
		return nil
	}

	slice := []string{}
	for _, val := range val {
		str, ok := val.(astString)
		if !ok {
			log.Warnf("%s: Requested []string, found %s", key, val)
			return nil
		}
		slice = append(slice, string(str))
	}

	return slice
}

// String returns the dsl in its code form.
func (dsl Dsl) String() string {
	return dsl.code
}

type dslError struct {
	pos scanner.Position
	err string
}

func (err dslError) Error() string {
	if err.pos.Filename == "" {
		return fmt.Sprintf("%d: %s", err.pos.Line, err.err)
	}
	return fmt.Sprintf("%s:%d: %s", err.pos.Filename, err.pos.Line, err.err)
}
