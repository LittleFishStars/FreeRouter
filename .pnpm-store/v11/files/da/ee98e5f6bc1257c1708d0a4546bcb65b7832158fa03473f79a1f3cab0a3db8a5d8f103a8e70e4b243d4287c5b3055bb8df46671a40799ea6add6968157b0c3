---
productId: material-ui
title: Number field React component
components: Button, IconButton, InputLabel, FormControl, FormLabel, FormHelperText, OutlinedInput
---

# Number Field

A React component for capturing numeric input from users.



A number field is an input with increment and decrement buttons for capturing numeric input from users.

Material UI does not include a number field component out of the box, but this page provides components composed with the [Base UI `NumberField`](https://base-ui.com/react/components/number-field) and styled to align with Material Design (MD2) specifications, so they can be used with Material UI.

As such, you must install Base UI before proceeding.
The examples that follow can then be copied and pasted directly into your app.
Note that Base UI is tree-shakeable, so the final bundle will only include the components used in your project.

## Installation

<codeblock storageKey="package-manager">

```bash npm
npm install @base-ui/react
```

```bash pnpm
pnpm add @base-ui/react
```

```bash yarn
yarn add @base-ui/react
```

</codeblock>

## Usage

1. Select one of the demos below that fits your visual design needs.
2. Click **Expand code** in the toolbar.
3. Select the file that starts with `./components/`.
4. Copy the code and paste it into your project.

## Outlined field

The outlined field uses the same building-block components as Material UI's outlined `TextField`—`FormControl`, `OutlinedInput`, `InputLabel`, and `FormHelperText`—with end adornments for the increment and decrement buttons.
See [Text Field—Components](/material-ui/react-text-field/#components) for more details.

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import NumberField from './components/NumberField';

export default function FieldDemo() {
  return (
    <Box sx={{ display: 'grid', gap: 4 }}>
      <NumberField label="Number Field" min={10} max={40} />
      <NumberField label="Number Field (Small)" size="small" />
      <NumberField
        label="Number Field with Error"
        min={10}
        max={40}
        defaultValue={100}
        size="small"
        error
      />
    </Box>
  );
}

```

## Spinner field

For the spinner field component, the increment and decrement buttons are placed next to the outlined input.
This is ideal for touch devices and narrow ranges of values.

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import NumberSpinner from './components/NumberSpinner';

export default function SpinnerDemo() {
  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        justifyContent: 'center',
      }}
    >
      <NumberSpinner label="Number Spinner" min={10} max={40} />
      <NumberSpinner label="Number Spinner (Small)" size="small" />
      <NumberSpinner
        label="Spinner with Error"
        min={10}
        max={40}
        defaultValue={100}
        size="small"
        error
      />
    </Box>
  );
}

```

## Units and currency

Both fields above use the input's end adornment for the increment and decrement buttons, so that slot is not available for a unit or currency symbol.
Use the [`format`](https://base-ui.com/react/components/number-field#NumberFieldRoot-format) prop instead—it accepts [`Intl.NumberFormatOptions`](https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Intl/NumberFormat/NumberFormat#options) and is forwarded to the Base UI `NumberField.Root`:

```jsx
<NumberField label="Price" format={{ style: 'currency', currency: 'USD' }} />
```

## Base UI API

See the documentation below for a complete reference to all of the props.

- [`NumberField`](https://base-ui.com/react/components/number-field#api-reference)


# Button API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Button Group](https://mui.com/material-ui/react-button-group/)
- [Button](https://mui.com/material-ui/react-button/)
- [Menubar](https://mui.com/material-ui/react-menubar/)
- [Number Field](https://mui.com/material-ui/react-number-field/)

## Import

```jsx
import Button from '@mui/material/Button';
// or
import { Button } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'inherit' \| 'primary' \| 'secondary' \| 'success' \| 'error' \| 'info' \| 'warning' \| string` | `'primary'` | No |  |
| component | `elementType` | - | No |  |
| disabled | `bool` | `false` | No |  |
| disableElevation | `bool` | `false` | No |  |
| disableFocusRipple | `bool` | `false` | No |  |
| disableRipple | `bool` | `false` | No |  |
| endIcon | `node` | - | No |  |
| fullWidth | `bool` | `false` | No |  |
| href | `string` | - | No |  |
| loading | `bool` | `null` | No |  |
| loadingIndicator | `node` | `<CircularProgress color="inherit" size={16} />` | No |  |
| loadingPosition | `'center' \| 'end' \| 'start'` | `'center'` | No |  |
| size | `'small' \| 'medium' \| 'large' \| string` | `'medium'` | No |  |
| startIcon | `node` | - | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| variant | `'contained' \| 'outlined' \| 'text' \| string` | `'text'` | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLButtonElement).

> Any other props supplied will be provided to the root element ([ButtonBase](https://mui.com/material-ui/api/button-base/)).

## Inheritance

While not explicitly documented above, the props of the [ButtonBase](https://mui.com/material-ui/api/button-base/) component are also available on Button.

## Theme default props

You can use `MuiButton` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | colorError | Styles applied to the root element if `color="error"`. |
| - | colorInfo | Styles applied to the root element if `color="info"`. |
| - | colorInherit | Styles applied to the root element if `color="inherit"`. |
| - | colorPrimary | Styles applied to the root element if `color="primary"`. |
| - | colorSecondary | Styles applied to the root element if `color="secondary"`. |
| - | colorSuccess | Styles applied to the root element if `color="success"`. |
| - | colorWarning | Styles applied to the root element if `color="warning"`. |
| - | contained | Styles applied to the root element if `variant="contained"`. |
| `.Mui-disabled` | - | State class applied to the root element if `disabled={true}`. |
| - | disableElevation | Styles applied to the root element if `disableElevation={true}`. |
| - | endIcon | Styles applied to the endIcon element if supplied. |
| `.Mui-focusVisible` | - | State class applied to the ButtonBase root element if the button is keyboard focused. |
| - | fullWidth | Styles applied to the root element if `fullWidth={true}`. |
| - | icon | Styles applied to the icon element if supplied |
| - | loading | Styles applied to the root element if `loading={true}`. |
| - | loadingIconPlaceholder | Styles applied to the loadingIconPlaceholder element. |
| - | loadingIndicator | Styles applied to the loadingIndicator element. |
| - | loadingPositionCenter | Styles applied to the root element if `loadingPosition="center"`. |
| - | loadingPositionEnd | Styles applied to the root element if `loadingPosition="end"`. |
| - | loadingPositionStart | Styles applied to the root element if `loadingPosition="start"`. |
| - | loadingWrapper | Styles applied to the loadingWrapper element. |
| - | outlined | Styles applied to the root element if `variant="outlined"`. |
| - | root | Styles applied to the root element. |
| - | sizeLarge | Styles applied to the root element if `size="large"`. |
| - | sizeMedium | Styles applied to the root element if `size="medium"`. |
| - | sizeSmall | Styles applied to the root element if `size="small"`. |
| - | startIcon | Styles applied to the startIcon element if supplied. |
| - | text | Styles applied to the root element if `variant="text"`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/Button/Button.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/Button/Button.js)

# FormControl API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Checkbox](https://mui.com/material-ui/react-checkbox/)
- [Number Field](https://mui.com/material-ui/react-number-field/)
- [Radio Group](https://mui.com/material-ui/react-radio-button/)
- [Switch](https://mui.com/material-ui/react-switch/)
- [Text Field](https://mui.com/material-ui/react-text-field/)

## Import

```jsx
import FormControl from '@mui/material/FormControl';
// or
import { FormControl } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'primary' \| 'secondary' \| 'error' \| 'info' \| 'success' \| 'warning' \| string` | `'primary'` | No |  |
| component | `elementType` | - | No |  |
| disabled | `bool` | `false` | No |  |
| error | `bool` | `false` | No |  |
| focused | `bool` | - | No |  |
| fullWidth | `bool` | `false` | No |  |
| hiddenLabel | `bool` | `false` | No |  |
| margin | `'dense' \| 'none' \| 'normal'` | `'none'` | No |  |
| required | `bool` | `false` | No |  |
| size | `'medium' \| 'small' \| string` | `'medium'` | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| variant | `'filled' \| 'outlined' \| 'standard'` | `'outlined'` | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLDivElement).

> Any other props supplied will be provided to the root element (native element).

## Theme default props

You can use `MuiFormControl` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | fullWidth | Styles applied to the root element if `fullWidth={true}`. |
| - | marginDense | Styles applied to the root element if `margin="dense"`. |
| - | marginNormal | Styles applied to the root element if `margin="normal"`. |
| - | root | Styles applied to the root element. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/FormControl/FormControl.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/FormControl/FormControl.js)

# FormHelperText API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Number Field](https://mui.com/material-ui/react-number-field/)
- [Text Field](https://mui.com/material-ui/react-text-field/)

## Import

```jsx
import FormHelperText from '@mui/material/FormHelperText';
// or
import { FormHelperText } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| component | `elementType` | - | No |  |
| disabled | `bool` | - | No |  |
| error | `bool` | - | No |  |
| filled | `bool` | - | No |  |
| focused | `bool` | - | No |  |
| margin | `'dense'` | - | No |  |
| required | `bool` | - | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| variant | `'filled' \| 'outlined' \| 'standard' \| string` | - | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLParagraphElement).

> Any other props supplied will be provided to the root element (native element).

## Theme default props

You can use `MuiFormHelperText` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | contained | Styles applied to the root element if `variant="filled"` or `variant="outlined"`. |
| `.Mui-disabled` | - | State class applied to the root element if `disabled={true}`. |
| `.Mui-error` | - | State class applied to the root element if `error={true}`. |
| - | filled | State class applied to the root element if `filled={true}`. |
| `.Mui-focused` | - | State class applied to the root element if `focused={true}`. |
| `.Mui-required` | - | State class applied to the root element if `required={true}`. |
| - | root | Styles applied to the root element. |
| - | sizeSmall | Styles applied to the root element if `size="small"`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/FormHelperText/FormHelperText.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/FormHelperText/FormHelperText.js)

# FormLabel API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Checkbox](https://mui.com/material-ui/react-checkbox/)
- [Number Field](https://mui.com/material-ui/react-number-field/)
- [Radio Group](https://mui.com/material-ui/react-radio-button/)
- [Switch](https://mui.com/material-ui/react-switch/)

## Import

```jsx
import FormLabel from '@mui/material/FormLabel';
// or
import { FormLabel } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'error' \| 'info' \| 'primary' \| 'secondary' \| 'success' \| 'warning' \| string` | - | No |  |
| component | `elementType` | - | No |  |
| disabled | `bool` | - | No |  |
| error | `bool` | - | No |  |
| filled | `bool` | - | No |  |
| focused | `bool` | - | No |  |
| required | `bool` | - | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |

> **Note**: The `ref` is forwarded to the root element (HTMLLabelElement).

> Any other props supplied will be provided to the root element (native element).

## Theme default props

You can use `MuiFormLabel` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | asterisk | Styles applied to the asterisk element. |
| - | colorSecondary | Styles applied to the root element if the color is secondary. |
| `.Mui-disabled` | - | State class applied to the root element if `disabled={true}`. |
| `.Mui-error` | - | State class applied to the root element if `error={true}`. |
| - | filled | State class applied to the root element if `filled={true}`. |
| `.Mui-focused` | - | State class applied to the root element if `focused={true}`. |
| `.Mui-required` | - | State class applied to the root element if `required={true}`. |
| - | root | Styles applied to the root element. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/FormLabel/FormLabel.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/FormLabel/FormLabel.js)

# IconButton API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Button](https://mui.com/material-ui/react-button/)
- [Number Field](https://mui.com/material-ui/react-number-field/)

## Import

```jsx
import IconButton from '@mui/material/IconButton';
// or
import { IconButton } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'inherit' \| 'default' \| 'primary' \| 'secondary' \| 'error' \| 'info' \| 'success' \| 'warning' \| string` | `'default'` | No |  |
| disabled | `bool` | `false` | No |  |
| disableFocusRipple | `bool` | `false` | No |  |
| disableRipple | `bool` | `false` | No |  |
| edge | `'end' \| 'start' \| false` | `false` | No |  |
| loading | `bool` | `null` | No |  |
| loadingIndicator | `node` | `<CircularProgress color="inherit" size={16} />` | No |  |
| size | `'small' \| 'medium' \| 'large' \| string` | `'medium'` | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |

> **Note**: The `ref` is forwarded to the root element (HTMLButtonElement).

> Any other props supplied will be provided to the root element ([ButtonBase](https://mui.com/material-ui/api/button-base/)).

## Inheritance

While not explicitly documented above, the props of the [ButtonBase](https://mui.com/material-ui/api/button-base/) component are also available on IconButton.

## Theme default props

You can use `MuiIconButton` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | colorError | Styles applied to the root element if `color="error"`. |
| - | colorInfo | Styles applied to the root element if `color="info"`. |
| - | colorInherit | Styles applied to the root element if `color="inherit"`. |
| - | colorPrimary | Styles applied to the root element if `color="primary"`. |
| - | colorSecondary | Styles applied to the root element if `color="secondary"`. |
| - | colorSuccess | Styles applied to the root element if `color="success"`. |
| - | colorWarning | Styles applied to the root element if `color="warning"`. |
| `.Mui-disabled` | - | State class applied to the root element if `disabled={true}`. |
| - | edgeEnd | Styles applied to the root element if `edge="end"`. |
| - | edgeStart | Styles applied to the root element if `edge="start"`. |
| - | loading | Styles applied to the root element if `loading={true}`. |
| - | loadingIndicator | Styles applied to the loadingIndicator element. |
| - | loadingWrapper | Styles applied to the loadingWrapper element. |
| - | root | Styles applied to the root element. |
| - | sizeLarge | Styles applied to the root element if `size="large"`. |
| - | sizeMedium | Styles applied to the root element if `size="medium"`. |
| - | sizeSmall | Styles applied to the root element if `size="small"`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/IconButton/IconButton.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/IconButton/IconButton.js)

# InputLabel API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Number Field](https://mui.com/material-ui/react-number-field/)
- [Text Field](https://mui.com/material-ui/react-text-field/)

## Import

```jsx
import InputLabel from '@mui/material/InputLabel';
// or
import { InputLabel } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| children | `node` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'error' \| 'info' \| 'primary' \| 'secondary' \| 'success' \| 'warning' \| string` | - | No |  |
| disableAnimation | `bool` | `false` | No |  |
| disabled | `bool` | - | No |  |
| error | `bool` | - | No |  |
| focused | `bool` | - | No |  |
| margin | `'dense'` | - | No |  |
| required | `bool` | - | No |  |
| shrink | `bool` | - | No |  |
| size | `'medium' \| 'small' \| string` | `'medium'` | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| variant | `'filled' \| 'outlined' \| 'standard'` | - | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLLabelElement).

> Any other props supplied will be provided to the root element ([FormLabel](https://mui.com/material-ui/api/form-label/)).

## Inheritance

While not explicitly documented above, the props of the [FormLabel](https://mui.com/material-ui/api/form-label/) component are also available on InputLabel.

## Theme default props

You can use `MuiInputLabel` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | animated | Styles applied to the input element unless `disableAnimation={true}`. |
| - | asterisk | State class applied to the asterisk element. |
| `.Mui-disabled` | - | State class applied to the root element if `disabled={true}`. |
| `.Mui-error` | - | State class applied to the root element if `error={true}`. |
| - | filled | Styles applied to the root element if `variant="filled"`. |
| `.Mui-focused` | - | State class applied to the root element if `focused={true}`. |
| - | formControl | Styles applied to the root element if the component is a descendant of `FormControl`. |
| - | outlined | Styles applied to the root element if `variant="outlined"`. |
| `.Mui-required` | - | State class applied to the root element if `required={true}`. |
| - | root | Styles applied to the root element. |
| - | shrink | Styles applied to the input element if `shrink={true}`. |
| - | sizeSmall | Styles applied to the root element if `size="small"`. |
| - | standard | Styles applied to the root element if `variant="standard"`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/InputLabel/InputLabel.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/InputLabel/InputLabel.js)

# OutlinedInput API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Number Field](https://mui.com/material-ui/react-number-field/)
- [Text Field](https://mui.com/material-ui/react-text-field/)

## Import

```jsx
import OutlinedInput from '@mui/material/OutlinedInput';
// or
import { OutlinedInput } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| autoComplete | `string` | - | No |  |
| autoFocus | `bool` | - | No |  |
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'primary' \| 'secondary' \| string` | - | No |  |
| defaultValue | `any` | - | No |  |
| disabled | `bool` | - | No |  |
| endAdornment | `node` | - | No |  |
| error | `bool` | - | No |  |
| fullWidth | `bool` | `false` | No |  |
| id | `string` | - | No |  |
| inputComponent | `elementType` | `'input'` | No |  |
| inputProps | `object` | `{}` | No |  |
| inputRef | `ref` | - | No |  |
| label | `node` | - | No |  |
| margin | `'dense' \| 'none'` | - | No |  |
| maxRows | `number \| string` | - | No |  |
| minRows | `number \| string` | - | No |  |
| multiline | `bool` | `false` | No |  |
| name | `string` | - | No |  |
| notched | `bool` | - | No |  |
| onChange | `function(event: React.ChangeEvent<HTMLTextAreaElement \| HTMLInputElement>) => void` | - | No |  |
| placeholder | `string` | - | No |  |
| readOnly | `bool` | - | No |  |
| required | `bool` | - | No |  |
| rows | `number \| string` | - | No |  |
| slotProps | `{ input?: object, notchedOutline?: func \| object, root?: object }` | `{}` | No |  |
| slots | `{ input?: elementType, notchedOutline?: elementType, root?: elementType }` | `{}` | No |  |
| startAdornment | `node` | - | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| type | `string` | `'text'` | No |  |
| value | `any` | - | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLDivElement).

> Any other props supplied will be provided to the root element ([InputBase](https://mui.com/material-ui/api/input-base/)).

## Inheritance

While not explicitly documented above, the props of the [InputBase](https://mui.com/material-ui/api/input-base/) component are also available on OutlinedInput.

## Theme default props

You can use `MuiOutlinedInput` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | adornedEnd | Styles applied to the root element if `endAdornment` is provided. |
| - | adornedStart | Styles applied to the root element if `startAdornment` is provided. |
| - | colorSecondary | Styles applied to the root element if the color is secondary. |
| `.Mui-disabled` | - | Styles applied to the root element if `disabled={true}`. |
| `.Mui-error` | - | State class applied to the root element if `error={true}`. |
| `.Mui-focused` | - | Styles applied to the root element if the component is focused. |
| - | input | Styles applied to the input element. |
| - | inputTypeSearch | Styles applied to the input element if `type="search"`. |
| - | multiline | Styles applied to the root element if `multiline={true}`. |
| - | notchedOutline | Styles applied to the NotchedOutline element. |
| - | root | Styles applied to the root element. |
| - | sizeSmall | Styles applied to the input element if `size="small"`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/OutlinedInput/OutlinedInput.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/OutlinedInput/OutlinedInput.js)