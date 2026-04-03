/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,ts}'],
  theme: {
    extend: {
      colors: {
        brand: { DEFAULT: '#1d4ed8', light: '#3b82f6', dark: '#1e3a8a' }
      }
    }
  },
  plugins: [require('@tailwindcss/forms')]
}
