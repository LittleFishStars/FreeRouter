"use strict";

var _interopRequireDefault = require("@babel/runtime/helpers/interopRequireDefault").default;
Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.default = mergeSlotProps;
var _isEventHandler = _interopRequireDefault(require("@mui/utils/isEventHandler"));
var _clsx = _interopRequireDefault(require("clsx"));
function mergeSlotProps(externalSlotProps, defaultSlotProps) {
  if (!externalSlotProps) {
    return defaultSlotProps;
  }
  function extractHandlers(externalSlotPropsValue, defaultSlotPropsValue) {
    const handlers = {};
    Object.keys(defaultSlotPropsValue).forEach(key => {
      if ((0, _isEventHandler.default)(key, defaultSlotPropsValue[key]) && typeof externalSlotPropsValue[key] === 'function') {
        // only compose the handlers if both default and external slot props match the event handler
        handlers[key] = (...args) => {
          externalSlotPropsValue[key](...args);
          defaultSlotPropsValue[key](...args);
        };
      }
    });
    return handlers;
  }
  if (typeof externalSlotProps === 'function' || typeof defaultSlotProps === 'function') {
    return ownerState => {
      const defaultSlotPropsValue = typeof defaultSlotProps === 'function' ? defaultSlotProps(ownerState) : defaultSlotProps;
      const externalSlotPropsValue = typeof externalSlotProps === 'function' ? externalSlotProps({
        ...ownerState,
        ...defaultSlotPropsValue
      }) : externalSlotProps;
      const className = (0, _clsx.default)(ownerState?.className, defaultSlotPropsValue?.className, externalSlotPropsValue?.className);
      const handlers = extractHandlers(externalSlotPropsValue, defaultSlotPropsValue);
      const result = {
        ...defaultSlotPropsValue,
        ...externalSlotPropsValue,
        ...handlers
      };
      if (className) {
        result.className = className;
      }
      if (defaultSlotPropsValue?.style && externalSlotPropsValue?.style) {
        result.style = {
          ...defaultSlotPropsValue.style,
          ...externalSlotPropsValue.style
        };
      }
      if (defaultSlotPropsValue?.sx && externalSlotPropsValue?.sx) {
        result.sx = [...(Array.isArray(defaultSlotPropsValue.sx) ? defaultSlotPropsValue.sx : [defaultSlotPropsValue.sx]), ...(Array.isArray(externalSlotPropsValue.sx) ? externalSlotPropsValue.sx : [externalSlotPropsValue.sx])];
      }
      return result;
    };
  }
  const typedDefaultSlotProps = defaultSlotProps;
  const handlers = extractHandlers(externalSlotProps, typedDefaultSlotProps);
  const className = (0, _clsx.default)(typedDefaultSlotProps?.className, externalSlotProps?.className);
  const result = {
    ...defaultSlotProps,
    ...externalSlotProps,
    ...handlers
  };
  if (className) {
    result.className = className;
  }
  if (typedDefaultSlotProps?.style && externalSlotProps?.style) {
    result.style = {
      ...typedDefaultSlotProps.style,
      ...externalSlotProps.style
    };
  }
  if (typedDefaultSlotProps?.sx && externalSlotProps?.sx) {
    result.sx = [...(Array.isArray(typedDefaultSlotProps.sx) ? typedDefaultSlotProps.sx : [typedDefaultSlotProps.sx]), ...(Array.isArray(externalSlotProps.sx) ? externalSlotProps.sx : [externalSlotProps.sx])];
  }
  return result;
}