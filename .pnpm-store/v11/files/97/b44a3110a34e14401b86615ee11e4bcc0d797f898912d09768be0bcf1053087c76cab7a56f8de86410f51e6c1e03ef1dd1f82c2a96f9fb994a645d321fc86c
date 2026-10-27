import type * as React from 'react';
import { type CreateMUIStyled as CreateMUIStyledStyledEngine, type CSSInterpolation } from '@mui/styled-engine';
import { type Theme as DefaultTheme } from "../createTheme/index.js";
import styleFunctionSx, { type SxProps } from "../styleFunctionSx/index.js";
export interface MUIStyledCommonProps<Theme extends object = DefaultTheme> {
  theme?: Theme | undefined;
  as?: React.ElementType | undefined;
  sx?: SxProps<Theme> | undefined;
}
export interface MuiStyledOptions {
  name?: string | undefined;
  slot?: string | undefined;
  overridesResolver?: ((props: any, styles: Record<string, CSSInterpolation>) => CSSInterpolation) | undefined;
  skipVariantsResolver?: boolean | undefined;
  skipSx?: boolean | undefined;
}
export type CreateMUIStyled<Theme extends object = DefaultTheme> = CreateMUIStyledStyledEngine<MUIStyledCommonProps<Theme>, MuiStyledOptions, Theme>;
export declare const systemDefaultTheme: DefaultTheme;
export declare function shouldForwardProp(prop: PropertyKey): boolean;
export default function createStyled<Theme extends object = DefaultTheme>(input?: {
  themeId?: string | undefined;
  defaultTheme?: Theme | undefined;
  rootShouldForwardProp?: ((prop: PropertyKey) => boolean) | undefined;
  slotShouldForwardProp?: ((prop: PropertyKey) => boolean) | undefined;
  styleFunctionSx?: typeof styleFunctionSx | undefined;
}): CreateMUIStyled<Theme>;