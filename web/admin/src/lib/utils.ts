import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// tailwind-merge has to know the design tokens' names (src/index.css), or it takes `text-body`
// for a colour and drops it next to `text-text-muted`.
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      text: ['title', 'heading', 'subheading', 'body', 'small', 'caption'],
      radius: ['control', 'card', 'bubble', 'pill'],
      shadow: ['raised', 'overlay'],
      ease: ['drawer'],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
