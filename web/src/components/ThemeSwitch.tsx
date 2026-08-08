import type { Theme } from '../lib/useTheme'
import { Icon, type IconName } from './Icon'

const CHOICES: { value: Theme; icon: IconName; label: string }[] = [
  { value: 'system', icon: 'monitor', label: '시스템 설정 따르기' },
  { value: 'light', icon: 'sun', label: '밝게' },
  { value: 'dark', icon: 'moon', label: '어둡게' },
]

/**
 * Three targets rather than one button that cycles.
 *
 * A cycling button cannot show which of the three is in force, and reaching the
 * one you want takes up to three presses to find out. Three buttons cost 60px
 * and answer the question by looking at it.
 */
export function ThemeSwitch({ theme, onChange }: { theme: Theme; onChange: (next: Theme) => void }) {
  return (
    <div className="seg" role="group" aria-label="화면 테마">
      {CHOICES.map((choice) => (
        <button
          key={choice.value}
          type="button"
          // aria-pressed rather than a radiogroup: these are three toggles in a
          // group, and a radiogroup would take the arrow keys hostage in a
          // header where Tab is what people use.
          aria-pressed={theme === choice.value}
          title={choice.label}
          aria-label={choice.label}
          onClick={() => onChange(choice.value)}
        >
          <Icon name={choice.icon} size={16} />
        </button>
      ))}
    </div>
  )
}
