import { useTheme } from '@/theme/ThemeProvider';
import { brand } from '@/theme/brand';

/** Automatically switch to the white logo on dark backgrounds. */
export function Logo({ variant = 'horizontal', className }: { variant?: 'horizontal' | 'mark'; className?: string }) {
  const { theme } = useTheme();
  const dark = theme === 'dark';
  const src =
    variant === 'mark'
      ? dark
        ? brand.logo.markWhite
        : brand.logo.markBlack
      : dark
        ? brand.logo.horizontalWhite
        : brand.logo.horizontalBlack;
  return <img src={src} alt="BSOS" className={className} draggable={false} />;
}
