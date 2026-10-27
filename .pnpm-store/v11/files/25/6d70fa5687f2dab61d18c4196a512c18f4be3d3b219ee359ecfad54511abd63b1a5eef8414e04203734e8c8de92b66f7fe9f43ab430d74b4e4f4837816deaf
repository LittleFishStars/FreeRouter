import { type CSSObject } from '@mui/styled-engine';
import { type Breakpoints, type BreakpointsOptions } from "../createBreakpoints/createBreakpoints.js";
import { type CssContainerQueries } from "../cssContainerQueries/index.js";
import { type Shape, type ShapeOptions } from "./shape.js";
import { type Spacing, type SpacingOptions } from "./createSpacing.js";
import { type ApplyStyles } from "./applyStyles.js";
import { type SxConfig, type SxProps } from "../styleFunctionSx/index.js";
export type { Breakpoint, Breakpoints, BreakpointOverrides } from "../createBreakpoints/createBreakpoints.js";
export type Direction = 'ltr' | 'rtl';
export interface Typography {}
export interface Mixins {}
export interface Shadows {}
export interface Transitions {}
export interface ZIndex {}
export interface ThemeOptions {
  shape?: ShapeOptions | undefined;
  breakpoints?: BreakpointsOptions | undefined;
  direction?: Direction | undefined;
  mixins?: Mixins | undefined;
  palette?: Record<string, any> | undefined;
  shadows?: Shadows | undefined;
  spacing?: SpacingOptions | undefined;
  transitions?: Transitions | undefined;
  components?: Record<string, any> | undefined;
  typography?: Typography | undefined;
  zIndex?: ZIndex | undefined;
  unstable_sxConfig?: SxConfig | undefined;
}
export interface Theme extends CssContainerQueries {
  shape: Shape;
  breakpoints: Breakpoints;
  direction: Direction;
  palette: Record<string, any> & {
    mode: 'light' | 'dark';
  };
  shadows?: Shadows | undefined;
  spacing: Spacing;
  transitions?: Transitions | undefined;
  components?: Record<string, any> | undefined;
  mixins?: Mixins | undefined;
  typography?: Typography | undefined;
  zIndex?: ZIndex | undefined;
  applyStyles: ApplyStyles<'light' | 'dark'>;
  unstable_sxConfig: SxConfig;
  unstable_sx: (props: SxProps<Theme>) => CSSObject;
}
/**
 * Generate a theme base on the options received.
 * @param options Takes an incomplete theme object and adds the missing parts.
 * @param args Deep merge the arguments with the about to be returned theme.
 * @returns A complete, ready-to-use theme object.
 */
declare function createTheme(options?: ThemeOptions, ...args: object[]): Theme;
export default createTheme;