package model

import (
	"fmt"
	"math/rand/v2"
	"time"
)

type Workspace struct {
	ID           string    `json:"id"`
	Version      uint64    `json:"version"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Workdir      string    `json:"workdir"`
	DefaultAgent string    `json:"defaultAgent,omitempty"`
	DefaultModel string    `json:"defaultModel,omitempty"`
	Color        string    `json:"color,omitempty"`
	Favorite     bool      `json:"favorite,omitempty"`
	SortOrder    int       `json:"sortOrder,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Deleted      bool      `json:"deleted,omitempty"`
}

func NewWorkspace(title, description, workdir string) *Workspace {
	return &Workspace{
		ID:          newWorkspaceID(),
		Version:     1,
		Title:       title,
		Description: description,
		Workdir:     workdir,
		Color:       RandomWorkspaceColor(),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func newWorkspaceID() string {
	t := time.Now()
	// Microsecond-resolution timestamp + a 32-bit random tag. Same-microsecond
	// collisions have ~1/2^32 probability per pair, which even under burst
	// workspace creation (batch imports, tests) effectively never triggers.
	return fmt.Sprintf("ws-%s-%06d-%08x", t.Format("20060102-150405"), t.Nanosecond()/1000, rand.Uint32())
}

// RandomWorkspaceColor returns a hex string (#rrggbb) with a random hue and
// pinned saturation/lightness so every workspace sits in a visually consistent
// band while still being distinct from its neighbours.
func RandomWorkspaceColor() string {
	return hslToHex(rand.IntN(360), 70, 55)
}

func hslToHex(h, s, l int) string {
	sf := float64(s) / 100
	lf := float64(l) / 100
	a := sf * min(lf, 1-lf)
	k := func(n float64) float64 {
		v := n + float64(h)/30
		return v - 12*float64(int(v)/12)
	}
	f := func(n float64) int {
		x := k(n)
		m := max(-1.0, min(x-3, min(9-x, 1)))
		return int(255*(lf-a*m) + 0.5)
	}
	return fmt.Sprintf("#%02x%02x%02x", f(0), f(8), f(4))
}
