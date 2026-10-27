import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** cn merges class names, with the later Tailwind class winning. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
