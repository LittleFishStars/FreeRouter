import deepmerge from '@mui/utils/deepmerge';
import createBreakpoints from "../createBreakpoints/createBreakpoints.mjs";
import cssContainerQueries from "../cssContainerQueries/index.mjs";
import shape from "./shape.mjs";
import createSpacing from "./createSpacing.mjs";
import styleFunctionSx from "../styleFunctionSx/styleFunctionSx.mjs";
import defaultSxConfig from "../styleFunctionSx/defaultSxConfig.mjs";
import applyStyles from "./applyStyles.mjs";
/**
 * Generate a theme base on the options received.
 * @param options Takes an incomplete theme object and adds the missing parts.
 * @param args Deep merge the arguments with the about to be returned theme.
 * @returns A complete, ready-to-use theme object.
 */
function createTheme(options = {}, ...args) {
  const {
    breakpoints: breakpointsInput = {},
    palette: paletteInput = {},
    spacing: spacingInput,
    shape: shapeInput = {},
    ...other
  } = options;
  const breakpoints = createBreakpoints(breakpointsInput);
  const spacing = createSpacing(spacingInput);
  let muiTheme = deepmerge({
    breakpoints,
    direction: 'ltr',
    components: {},
    // Inject component definitions.
    palette: {
      mode: 'light',
      ...paletteInput
    },
    spacing,
    shape: {
      ...shape,
      ...shapeInput
    }
  }, other);
  muiTheme = cssContainerQueries(muiTheme);
  muiTheme.applyStyles = applyStyles;
  muiTheme = args.reduce((acc, argument) => deepmerge(acc, argument), muiTheme);
  muiTheme.unstable_sxConfig = {
    ...defaultSxConfig,
    ...other?.unstable_sxConfig
  };
  muiTheme.unstable_sx = function sx(props) {
    return styleFunctionSx({
      sx: props,
      theme: this
    });
  };
  muiTheme.internal_cache = {};
  return muiTheme;
}
export default createTheme;