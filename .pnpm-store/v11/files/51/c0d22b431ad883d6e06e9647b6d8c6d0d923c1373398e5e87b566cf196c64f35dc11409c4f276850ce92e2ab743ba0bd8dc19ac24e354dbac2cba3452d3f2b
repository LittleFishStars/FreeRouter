import borders from "../borders/index.mjs";
import display from "../display/index.mjs";
import flexbox from "../flexbox/index.mjs";
import grid from "../cssGrid/index.mjs";
import positions from "../positions/index.mjs";
import palette from "../palette/index.mjs";
import shadows from "../shadows/index.mjs";
import sizing from "../sizing/index.mjs";
import spacing from "../spacing/index.mjs";
import typography from "../typography/index.mjs";
/** @internal */
export const styleFunctionMapping = {
  borders,
  display,
  flexbox,
  grid,
  positions,
  palette,
  shadows,
  sizing,
  spacing,
  typography
};

/** @internal */
export const propToStyleFunction = Object.keys(styleFunctionMapping).reduce((acc, styleFnName) => {
  const styleFunction = styleFunctionMapping[styleFnName];
  for (const propName of styleFunction.filterProps) {
    acc[propName] = styleFunction;
  }
  return acc;
}, {});
function getThemeValue(prop, value, theme) {
  const inputProps = {
    [prop]: value,
    theme
  };
  const styleFunction = propToStyleFunction[prop];
  return styleFunction ? styleFunction(inputProps) : {
    [prop]: value
  };
}
export default getThemeValue;