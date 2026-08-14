import { fireEvent, render } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { VirtualList } from './VirtualList'

const ITEM_HEIGHT = 20
const ITEM_COUNT = 100
const VIEWPORT = 200
const CONTENT = ITEM_COUNT * ITEM_HEIGHT // 2000

function renderList(onEndReached: () => void) {
  const items = Array.from({ length: ITEM_COUNT }, (_, i) => `row-${i}`)
  const utils = render(
    <VirtualList
      items={items}
      itemHeight={ITEM_HEIGHT}
      maxHeight={VIEWPORT}
      onEndReached={onEndReached}
      renderItem={(item) => <span>{item}</span>}
    />,
  )
  const scroller = utils.container.firstElementChild as HTMLElement
  return { scroller, ...utils }
}

function scrollTo(scroller: HTMLElement, scrollTop: number) {
  Object.defineProperty(scroller, 'scrollTop', { value: scrollTop, configurable: true, writable: true })
  Object.defineProperty(scroller, 'clientHeight', { value: VIEWPORT, configurable: true, writable: true })
  Object.defineProperty(scroller, 'scrollHeight', { value: CONTENT, configurable: true, writable: true })
  fireEvent.scroll(scroller)
}

describe('VirtualList onEndReached', () => {
  it('fires once per bottom visit and re-arms after leaving the bottom zone', () => {
    const onEndReached = vi.fn()
    const { scroller } = renderList(onEndReached)

    // Near the bottom (distanceToBottom = 0 < 200 threshold) → fires once.
    scrollTo(scroller, CONTENT - VIEWPORT)
    expect(onEndReached).toHaveBeenCalledTimes(1)

    // Further scroll ticks at the bottom don't spam.
    scrollTo(scroller, CONTENT - VIEWPORT)
    scrollTo(scroller, CONTENT - VIEWPORT - 50)
    expect(onEndReached).toHaveBeenCalledTimes(1)

    // The load-more triggered above produced no new items (e.g. the fetch
    // failed), so items.length never changed. Scrolling away from the bottom
    // zone and back must re-arm the trigger, otherwise auto-load is dead
    // until the next successful page.
    scrollTo(scroller, 0)
    scrollTo(scroller, CONTENT - VIEWPORT)
    expect(onEndReached).toHaveBeenCalledTimes(2)
  })

  it('re-arms when items grow even without leaving the bottom zone', () => {
    const onEndReached = vi.fn()
    const { scroller, rerender } = renderList(onEndReached)

    scrollTo(scroller, CONTENT - VIEWPORT)
    expect(onEndReached).toHaveBeenCalledTimes(1)

    const moreItems = Array.from({ length: ITEM_COUNT + 20 }, (_, i) => `row-${i}`)
    rerender(
      <VirtualList
        items={moreItems}
        itemHeight={ITEM_HEIGHT}
        maxHeight={VIEWPORT}
        onEndReached={onEndReached}
        renderItem={(item) => <span>{item}</span>}
      />,
    )

    scrollTo(scroller, CONTENT - VIEWPORT)
    expect(onEndReached).toHaveBeenCalledTimes(2)
  })
})
