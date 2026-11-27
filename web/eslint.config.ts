import antfu from '@antfu/eslint-config'
import solid from 'eslint-plugin-solid/configs/v2'

// The solid option of the antfu config targets Solid 1, so the Solid 2
// rules are added by hand (ADR 0003).
export default antfu(
  {
    typescript: true,
    ignores: ['build/**', 'headless/**'],
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    ...solid,
  },
)
