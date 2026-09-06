package handler

import (
	"context"
	"fmt"

	resvg "github.com/kanrichan/resvg-go"
)

const rasterizedIconSize = 256

func rasterizeSVGIcon(ctx context.Context, svg []byte) ([]byte, error) {
	renderContext, err := resvg.NewContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("create SVG render context failed: %w", err)
	}
	defer renderContext.Close()

	renderer, err := renderContext.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("create SVG renderer failed: %w", err)
	}
	defer renderer.Close()

	png, err := renderer.RenderWithSize(svg, rasterizedIconSize, rasterizedIconSize)
	if err != nil {
		return nil, fmt.Errorf("render SVG as PNG failed: %w", err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("render SVG as PNG failed: renderer returned empty data")
	}
	return png, nil
}
