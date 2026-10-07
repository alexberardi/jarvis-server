// Registers jest-dom's matchers (toBeInTheDocument and friends) with vitest's
// Assertion type. It lives under src/ because tsconfig.app.json includes only
// that directory, so an import in tests/setup.ts never reaches tsc -- the tests
// ran fine while `tsc -b` failed on every matcher.
import '@testing-library/jest-dom/vitest'
