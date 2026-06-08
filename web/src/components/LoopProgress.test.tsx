import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { LoopProgress } from './LoopProgress'
import type { JobProgress } from '../types'

const baseProgress: JobProgress = {
  totalSteps: 10,
  completedCount: 3,
  failedCount: 0,
  currentPath: [0, 0, 0, 0],
}

describe('LoopProgress conditional judge display', () => {
  it('renders the latest judge decision round but no decision label when continuing', () => {
    const progress: JobProgress = {
      ...baseProgress,
      lastJudgeDecision: {
        path: [0, 0],
        stop: false,
        reason: 'tests still failing, keep going',
        iteration: 2,
        maxIterations: 10,
      },
    }
    render(<LoopProgress progress={progress} status="running" />)

    const judge = screen.getByTestId('loop-progress-judge')
    expect(judge).toHaveAttribute('data-judge-stop', 'false')
    expect(judge).toHaveTextContent('Round 2 / 10')
    // A "continue" decision shows no label — only STOP is surfaced.
    expect(judge.querySelector('.loop-progress-judge-decision')).not.toBeInTheDocument()
  })

  it('renders STOP and expands the reason on click', async () => {
    const user = userEvent.setup()
    const progress: JobProgress = {
      ...baseProgress,
      lastJudgeDecision: {
        path: [0, 0],
        stop: true,
        reason: 'all tests pass now',
        iteration: 4,
        maxIterations: 10,
      },
    }
    render(<LoopProgress progress={progress} status="completed" />)

    const judge = screen.getByTestId('loop-progress-judge')
    expect(judge).toHaveAttribute('data-judge-stop', 'true')
    expect(judge).toHaveTextContent('STOP')

    // Reason is hidden until expanded.
    expect(screen.queryByText('all tests pass now')).not.toBeInTheDocument()
    await user.click(judge.querySelector('.loop-progress-judge-header')!)
    expect(screen.getByText('all tests pass now')).toBeInTheDocument()
  })

  it('does not render the judge block when there is no decision', () => {
    render(<LoopProgress progress={baseProgress} status="running" />)
    expect(screen.queryByTestId('loop-progress-judge')).not.toBeInTheDocument()
  })
})
