'use client';

import * as React from 'react';
import PropTypes from 'prop-types';
import { useRtl } from '@mui/system/RtlProvider';
import composeClasses from '@mui/utils/composeClasses';
import clsx from 'clsx';
import { styled } from "../zero-styled/index.mjs";
import { useDefaultProps } from "../DefaultPropsProvider/index.mjs";
import KeyboardArrowLeft from "../internal/svg-icons/KeyboardArrowLeft.mjs";
import KeyboardArrowRight from "../internal/svg-icons/KeyboardArrowRight.mjs";
import IconButton from "../IconButton/index.mjs";
import Tooltip from "../Tooltip/index.mjs";
import LastPageIconDefault from "../internal/svg-icons/LastPage.mjs";
import FirstPageIconDefault from "../internal/svg-icons/FirstPage.mjs";
import getActiveElement from "../utils/getActiveElement.mjs";
import ownerDocument from "../utils/ownerDocument.mjs";
import useEnhancedEffect from "../utils/useEnhancedEffect.mjs";
import useForkRef from "../utils/useForkRef.mjs";
import { getTablePaginationActionsUtilityClass } from "./tablePaginationActionsClasses.mjs";

// Custom slots can forward this attribute to participate without forwarding a ref.
import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
const actionSelector = '[data-mui-pagination-action]';
function isDisabled(button) {
  return button.matches(':disabled, [aria-disabled="true"]');
}
const useUtilityClasses = ownerState => {
  const {
    classes
  } = ownerState;
  const slots = {
    root: ['root']
  };
  return composeClasses(slots, getTablePaginationActionsUtilityClass, classes);
};
const TablePaginationActionsRoot = styled('div', {
  name: 'MuiTablePaginationActions',
  slot: 'Root'
})({});
const TablePaginationActionsButton = /*#__PURE__*/React.forwardRef(function TablePaginationActionsButton({
  'aria-label': ariaLabel,
  title,
  ...other
}, ref) {
  return /*#__PURE__*/_jsx(Tooltip, {
    title: title,
    children: /*#__PURE__*/_jsx(IconButton, {
      "aria-label": ariaLabel ?? title,
      ...other,
      ref: ref
    })
  });
});
process.env.NODE_ENV !== "production" ? TablePaginationActionsButton.propTypes /* remove-proptypes */ = {
  'aria-label': PropTypes.string,
  title: PropTypes.node
} : void 0;
const TablePaginationActions = /*#__PURE__*/React.forwardRef(function TablePaginationActions(inProps, ref) {
  const props = useDefaultProps({
    props: inProps,
    name: 'MuiTablePaginationActions'
  });
  const {
    className,
    count,
    disabled = false,
    getItemAriaLabel,
    onPageChange,
    page,
    rowsPerPage,
    showFirstButton,
    showLastButton,
    slots = {},
    slotProps = {},
    ...other
  } = props;
  const isRtl = useRtl();
  const ownerState = props;
  const classes = useUtilityClasses(ownerState);

  // Refs needed for managing focus whenever a button becomes disabled.
  const rootRef = React.useRef(null);
  const handleRef = useForkRef(rootRef, ref);
  const focusedButtonRef = React.useRef(null);
  const handleFocus = event => {
    focusedButtonRef.current = event.target.matches(actionSelector) ? event.target : null;
    other.onFocus?.(event);
  };
  const handleBlur = event => {
    // Disabling a focused button can blur it before the layout effect runs.
    if (!isDisabled(event.target) || event.relatedTarget != null) {
      focusedButtonRef.current = null;
    }
    other.onBlur?.(event);
  };

  // Check if focused button is disabled and move focus to the next available button.
  useEnhancedEffect(() => {
    const focusedButton = focusedButtonRef.current;
    if (!focusedButton || !isDisabled(focusedButton)) {
      return;
    }
    focusedButtonRef.current = null;
    const document = ownerDocument(focusedButton);
    const activeElement = getActiveElement(document);
    // Preserve focus moved elsewhere by the user or an application handler.
    if (activeElement != null && activeElement !== focusedButton && activeElement !== document.body && activeElement !== document.documentElement) {
      return;
    }
    const buttons = Array.from(rootRef.current.querySelectorAll(actionSelector));
    const index = buttons.indexOf(focusedButton);
    if (index === -1) {
      return;
    }

    // Prefer following actions, then fall back to preceding actions in reverse order.
    const nextButton = [...buttons.slice(index + 1), ...buttons.slice(0, index).reverse()].find(button => !isDisabled(button) && button.tabIndex >= 0);
    nextButton?.focus();
  });
  const handleFirstPageButtonClick = event => {
    onPageChange(event, 0);
  };
  const handleBackButtonClick = event => {
    onPageChange(event, page - 1);
  };
  const handleNextButtonClick = event => {
    onPageChange(event, page + 1);
  };
  const handleLastPageButtonClick = event => {
    onPageChange(event, Math.max(0, Math.ceil(count / rowsPerPage) - 1));
  };
  const FirstButton = slots.firstButton ?? TablePaginationActionsButton;
  const LastButton = slots.lastButton ?? TablePaginationActionsButton;
  const NextButton = slots.nextButton ?? TablePaginationActionsButton;
  const PreviousButton = slots.previousButton ?? TablePaginationActionsButton;
  const FirstButtonIcon = slots.firstButtonIcon ?? FirstPageIconDefault;
  const LastButtonIcon = slots.lastButtonIcon ?? LastPageIconDefault;
  const NextButtonIcon = slots.nextButtonIcon ?? KeyboardArrowRight;
  const PreviousButtonIcon = slots.previousButtonIcon ?? KeyboardArrowLeft;
  const FirstButtonSlot = isRtl ? LastButton : FirstButton;
  const PreviousButtonSlot = isRtl ? NextButton : PreviousButton;
  const NextButtonSlot = isRtl ? PreviousButton : NextButton;
  const LastButtonSlot = isRtl ? FirstButton : LastButton;
  const firstButtonSlotProps = isRtl ? slotProps.lastButton : slotProps.firstButton;
  const previousButtonSlotProps = isRtl ? slotProps.nextButton : slotProps.previousButton;
  const nextButtonSlotProps = isRtl ? slotProps.previousButton : slotProps.nextButton;
  const lastButtonSlotProps = isRtl ? slotProps.firstButton : slotProps.lastButton;
  return /*#__PURE__*/_jsxs(TablePaginationActionsRoot, {
    ref: handleRef,
    className: clsx(classes.root, className),
    ...other,
    onFocus: handleFocus,
    onBlur: handleBlur,
    children: [showFirstButton && /*#__PURE__*/_jsx(FirstButtonSlot, {
      onClick: handleFirstPageButtonClick,
      disabled: disabled || page === 0,
      "aria-label": getItemAriaLabel('first', page),
      title: getItemAriaLabel('first', page),
      "data-mui-pagination-action": "first",
      ...firstButtonSlotProps,
      children: isRtl ? /*#__PURE__*/_jsx(LastButtonIcon, {
        ...slotProps.lastButtonIcon
      }) : /*#__PURE__*/_jsx(FirstButtonIcon, {
        ...slotProps.firstButtonIcon
      })
    }), /*#__PURE__*/_jsx(PreviousButtonSlot, {
      onClick: handleBackButtonClick,
      disabled: disabled || page === 0,
      color: "inherit",
      "aria-label": getItemAriaLabel('previous', page),
      title: getItemAriaLabel('previous', page),
      "data-mui-pagination-action": "previous",
      ...previousButtonSlotProps,
      children: isRtl ? /*#__PURE__*/_jsx(NextButtonIcon, {
        ...slotProps.nextButtonIcon
      }) : /*#__PURE__*/_jsx(PreviousButtonIcon, {
        ...slotProps.previousButtonIcon
      })
    }), /*#__PURE__*/_jsx(NextButtonSlot, {
      onClick: handleNextButtonClick,
      disabled: disabled || (count !== -1 ? page >= Math.ceil(count / rowsPerPage) - 1 : false),
      color: "inherit",
      "aria-label": getItemAriaLabel('next', page),
      title: getItemAriaLabel('next', page),
      "data-mui-pagination-action": "next",
      ...nextButtonSlotProps,
      children: isRtl ? /*#__PURE__*/_jsx(PreviousButtonIcon, {
        ...slotProps.previousButtonIcon
      }) : /*#__PURE__*/_jsx(NextButtonIcon, {
        ...slotProps.nextButtonIcon
      })
    }), showLastButton && /*#__PURE__*/_jsx(LastButtonSlot, {
      onClick: handleLastPageButtonClick,
      disabled: disabled || page >= Math.ceil(count / rowsPerPage) - 1,
      "aria-label": getItemAriaLabel('last', page),
      title: getItemAriaLabel('last', page),
      "data-mui-pagination-action": "last",
      ...lastButtonSlotProps,
      children: isRtl ? /*#__PURE__*/_jsx(FirstButtonIcon, {
        ...slotProps.firstButtonIcon
      }) : /*#__PURE__*/_jsx(LastButtonIcon, {
        ...slotProps.lastButtonIcon
      })
    })]
  });
});
process.env.NODE_ENV !== "production" ? TablePaginationActions.propTypes /* remove-proptypes */ = {
  // ┌────────────────────────────── Warning ──────────────────────────────┐
  // │ These PropTypes are generated from the TypeScript type definitions. │
  // │    To update them, edit the d.ts file and run `pnpm proptypes`.     │
  // └─────────────────────────────────────────────────────────────────────┘
  /**
   * @ignore
   */
  children: PropTypes.node,
  /**
   * Override or extend the styles applied to the component.
   */
  classes: PropTypes.object,
  /**
   * @ignore
   */
  className: PropTypes.string,
  /**
   * @ignore
   */
  count: PropTypes.number.isRequired,
  /**
   * If `true`, the component is disabled.
   * @default false
   */
  disabled: PropTypes.bool,
  /**
   * Accepts a function which returns a string value that provides a user-friendly name for the current page.
   * This is important for screen reader users.
   *
   * For localization purposes, you can use the provided [translations](https://mui.com/material-ui/guides/localization/).
   * @param {string} type The link or button type to format ('first' | 'last' | 'next' | 'previous').
   * @returns {string}
   */
  getItemAriaLabel: PropTypes.func.isRequired,
  /**
   * @ignore
   */
  onBlur: PropTypes.func,
  /**
   * @ignore
   */
  onFocus: PropTypes.func,
  /**
   * @ignore
   */
  onPageChange: PropTypes.func.isRequired,
  /**
   * @ignore
   */
  page: PropTypes.number.isRequired,
  /**
   * @ignore
   */
  rowsPerPage: PropTypes.number.isRequired,
  /**
   * @ignore
   */
  showFirstButton: PropTypes.bool.isRequired,
  /**
   * @ignore
   */
  showLastButton: PropTypes.bool.isRequired,
  /**
   * @ignore
   */
  slotProps: PropTypes.shape({
    firstButton: PropTypes.object,
    firstButtonIcon: PropTypes.object,
    lastButton: PropTypes.object,
    lastButtonIcon: PropTypes.object,
    nextButton: PropTypes.object,
    nextButtonIcon: PropTypes.object,
    previousButton: PropTypes.object,
    previousButtonIcon: PropTypes.object
  }),
  /**
   * @ignore
   */
  slots: PropTypes.shape({
    firstButton: PropTypes.elementType,
    firstButtonIcon: PropTypes.elementType,
    lastButton: PropTypes.elementType,
    lastButtonIcon: PropTypes.elementType,
    nextButton: PropTypes.elementType,
    nextButtonIcon: PropTypes.elementType,
    previousButton: PropTypes.elementType,
    previousButtonIcon: PropTypes.elementType
  }),
  /**
   * The system prop that allows defining system overrides as well as additional CSS styles.
   */
  sx: PropTypes.oneOfType([PropTypes.arrayOf(PropTypes.oneOfType([PropTypes.func, PropTypes.object, PropTypes.bool])), PropTypes.func, PropTypes.object])
} : void 0;
export default TablePaginationActions;