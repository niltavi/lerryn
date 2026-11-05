import { defineConfig } from 'vitest/config';

// Pure modules run in Node. Hook tests select jsdom and mock native boundaries.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.{ts,tsx}'],
  },
});
