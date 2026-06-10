import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { LoopProgress } from './LoopProgress'
import type { JobProgress, FlowNode } from '../types'

const baseProgress: JobProgress = {
  totalSteps: 10,
  completedCount: 3,
  failedCount: 0,
  currentPath: [0, 0, 0, 0],
}

describe('LoopProgress', () => {
  it('does not render a judge block (evaluator output lives in the chat stream)', () => {
    render(<LoopProgress progress={baseProgress} status="running" />)
    expect(screen.queryByTestId('loop-progress-judge')).not.toBeInTheDocument()
  })

  it('reflects groupActualIterations in the session/step denominator after an early stop', () => {
    // A group with an evaluator, cap 10, one prompt step + one evaluator step.
    const flow: FlowNode[] = [
      {
        id: 'g',
        type: 'group',
        iterationCount: 10,
        children: [
          { id: 's', type: 'step', message: 'work', repeatCount: 1, roundMode: 'beforeRound', roundType: 'prompt' },
          { id: 'e', type: 'step', message: 'done?', repeatCount: 1, roundMode: 'none', roundType: 'evaluator' },
        ],
      },
    ]
    // The group stopped early after 2 actual rounds.
    const progress: JobProgress = {
      totalSteps: 4,
      completedCount: 4,
      failedCount: 0,
      currentPath: [0, 1, 1, 0],
      groupActualIterations: { '0': 2 },
    }
    render(<LoopProgress progress={progress} status="completed" flow={flow} />)
    // 2 rounds × beforeRound business step = 2 sessions total.
    expect(screen.getByTestId('loop-progress-session')).toHaveTextContent('Session 2 / 2')
  })

  it('trims siblings skipped after STOP inside the final actual group iteration', () => {
    const flow: FlowNode[] = [
      {
        id: 'g',
        type: 'group',
        iterationCount: 5,
        children: [
          { id: 'stop', type: 'step', message: 'stop', repeatCount: 1, roundMode: 'beforeRound', roundType: 'prompt' },
          { id: 'skipped', type: 'step', message: 'skip', repeatCount: 1, roundMode: 'none', roundType: 'prompt' },
        ],
      },
    ]
    const progress: JobProgress = {
      totalSteps: 1,
      completedCount: 1,
      failedCount: 0,
      currentPath: [0, 0, 0, 0],
      groupActualIterations: { '0': 1 },
      groupActualLeafCounts: { '0': 1 },
    }

    render(<LoopProgress progress={progress} status="completed" flow={flow} />)

    expect(screen.getByTestId('loop-progress-session')).toHaveTextContent('Session 1 / 1')
    expect(screen.getByTestId('loop-progress-step')).toHaveTextContent('Step 1 / 1')
  })
})
