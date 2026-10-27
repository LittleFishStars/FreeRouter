# Palette

The palette enables you to modify the color of the components to suit your brand.

## Color tokens

Palette colors are represented by four tokens:

- `main`: The main shade of the color
- `light`: A lighter shade of `main`
- `dark`: A darker shade of `main`
- `contrastText`: Text color, intended to contrast with `main`

Here's how Material UI's default theme defines the primary color tokens:

```js
const primary = {
  main: '#1976d2',
  light: '#42a5f5',
  dark: '#1565c0',
  contrastText: '#fff',
};
```

See the [Color](/material-ui/customization/color/) documentation for details on the Material Design color system.

## Default colors

The theme exposes the following default palette colors (accessible under `theme.palette.*`):

- `primary` - for primary interface elements.
- `secondary` - for secondary interface elements.
- `error` - for elements that the user should be made aware of.
- `warning` - for potentially dangerous actions or important messages.
- `info` - for highlighting neutral information.
- `success` - for indicating the successful completion of an action that the user triggered.

See Material Design's [Color System](https://m2.material.io/design/color/the-color-system.html) for details on color usage and guidelines.

### Values

You can explore the default palette values using [the theme explorer](/material-ui/customization/default-theme/?expand-path=$.palette), or by opening the dev tools console on this page (`window.theme.palette`).

```jsx
import Box from '@mui/material/Box';
import Grid from '@mui/material/Grid';
import Typography from '@mui/material/Typography';
import { useTheme, rgbToHex, styled } from '@mui/material/styles';

const Group = styled(Typography)(({ theme }) => ({
  marginTop: theme.spacing(3),
}));

const Color = styled(Grid)(({ theme }) => ({
  display: 'flex',
  alignItems: 'center',
  '& div:first-of-type': {
    width: theme.spacing(6),
    height: theme.spacing(6),
    marginRight: theme.spacing(1),
    borderRadius: theme.shape.borderRadius,
    boxShadow: 'inset 0 2px 4px 0 rgba(0, 0, 0, .06)',
  },
}));

export default function Intentions() {
  const theme = useTheme();

  const item = (color, name) => (
    <Color size={{ xs: 12, sm: 6, md: 4 }}>
      <div style={{ backgroundColor: color }} />
      <div>
        <Typography variant="body2">{name}</Typography>
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          {rgbToHex(color)}
        </Typography>
      </div>
    </Color>
  );

  return (
    <Box sx={{ width: '100%' }}>
      <Group gutterBottom>Primary</Group>
      <Grid container spacing={2}>
        {item(theme.palette.primary.light, 'palette.primary.light')}
        {item(theme.palette.primary.main, 'palette.primary.main')}
        {item(theme.palette.primary.dark, 'palette.primary.dark')}
      </Grid>
      <Group gutterBottom>Secondary</Group>
      <Grid container spacing={2}>
        {item(theme.palette.secondary.light, 'palette.secondary.light')}
        {item(theme.palette.secondary.main, 'palette.secondary.main')}
        {item(theme.palette.secondary.dark, 'palette.secondary.dark')}
      </Grid>
      <Group gutterBottom>Error</Group>
      <Grid container spacing={2}>
        {item(theme.palette.error.light, 'palette.error.light')}
        {item(theme.palette.error.main, 'palette.error.main')}
        {item(theme.palette.error.dark, 'palette.error.dark')}
      </Grid>
      <Group gutterBottom>Warning</Group>
      <Grid container spacing={2}>
        {item(theme.palette.warning.light, 'palette.warning.light')}
        {item(theme.palette.warning.main, 'palette.warning.main')}
        {item(theme.palette.warning.dark, 'palette.warning.dark')}
      </Grid>
      <Group gutterBottom>Info</Group>
      <Grid container spacing={2}>
        {item(theme.palette.info.light, 'palette.info.light')}
        {item(theme.palette.info.main, 'palette.info.main')}
        {item(theme.palette.info.dark, 'palette.info.dark')}
      </Grid>
      <Group gutterBottom>Success</Group>
      <Grid container spacing={2}>
        {item(theme.palette.success.light, 'palette.success.light')}
        {item(theme.palette.success.main, 'palette.success.main')}
        {item(theme.palette.success.dark, 'palette.success.dark')}
      </Grid>
    </Box>
  );
}

```

The default palette uses the shades prefixed with `A` (`A200`, etc.) for the secondary palette color,
and the un-prefixed shades for the other palette colors.

### Customization

You may override the default palette values by including a palette object as part of your theme.
If any of the:

- [`.palette.primary`](/material-ui/customization/default-theme/?expand-path=$.palette.primary)
- [`.palette.secondary`](/material-ui/customization/default-theme/?expand-path=$.palette.secondary)
- [`.palette.error`](/material-ui/customization/default-theme/?expand-path=$.palette.error)
- [`.palette.warning`](/material-ui/customization/default-theme/?expand-path=$.palette.warning)
- [`.palette.info`](/material-ui/customization/default-theme/?expand-path=$.palette.info)
- [`.palette.success`](/material-ui/customization/default-theme/?expand-path=$.palette.success)

palette color objects are provided, they will replace the default ones.

This can be achieved by either using a color object or by providing the colors directly:

#### Using a color object

The most direct way to customize a palette color is to import and apply one or more [color objects](/material-ui/customization/color/#2014-material-design-color-palettes), as shown below:

```tsx
import { createTheme, ThemeProvider } from '@mui/material/styles';
import { lime, purple } from '@mui/material/colors';
import Button from '@mui/material/Button';

const theme = createTheme({
  palette: {
    primary: lime,
    secondary: purple,
  },
});

export default function UsingColorObject() {
  return (
    <ThemeProvider theme={theme}>
      <Button variant="contained">Primary</Button>
      <Button variant="contained" color="secondary" sx={{ ml: 2 }}>
        Secondary
      </Button>
    </ThemeProvider>
  );
}

```

#### Providing the colors directly

To modify each color directly, provide an object with one or more of the color tokens.
Only the `main` token is required; `light`, `dark`, and `contrastText` are optional, and if not provided, then their values are calculated automatically:

```js
import { createTheme } from '@mui/material/styles';

const theme = createTheme({
  palette: {
    primary: {
      main: '#FF5733',
      // light: will be calculated from palette.primary.main,
      // dark: will be calculated from palette.primary.main,
      // contrastText: will be calculated to contrast with palette.primary.main
    },
    secondary: {
      main: '#E0C2FF',
      light: '#F5EBFF',
      // dark: will be calculated from palette.secondary.main,
      contrastText: '#47008F',
    },
  },
});
```

```tsx
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Button from '@mui/material/Button';
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import { unstable_capitalize as capitalize } from '@mui/utils';
import Typography from '@mui/material/Typography';

const theme = createTheme({
  palette: {
    primary: {
      main: '#FF5733',
      // light: will be calculated from palette.primary.main,
      // dark: will be calculated from palette.primary.main,
      // contrastText: will be calculated to contrast with palette.primary.main
    },
    secondary: {
      main: '#E0C2FF',
      light: '#F5EBFF',
      // dark: will be calculated from palette.secondary.main,
      contrastText: '#47008F',
    },
  },
});

function ColorShowcase({ color }: { color: 'primary' | 'secondary' }) {
  return (
    <Stack sx={{ gap: 2, alignItems: 'center' }}>
      <Button variant="contained" color={color}>
        {capitalize(color)}
      </Button>
      <Stack direction="row" sx={{ gap: 1 }}>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">light</Typography>
          <Box sx={{ bgcolor: `${color}.light`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">main</Typography>
          <Box sx={{ bgcolor: `${color}.main`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">dark</Typography>
          <Box sx={{ bgcolor: `${color}.dark`, width: 40, height: 20 }} />
        </Stack>
      </Stack>
    </Stack>
  );
}

export default function ManuallyProvidePaletteColor() {
  return (
    <ThemeProvider theme={theme}>
      <Stack direction="row" sx={{ gap: 8 }}>
        <ColorShowcase color="primary" />
        <ColorShowcase color="secondary" />
      </Stack>
    </ThemeProvider>
  );
}

```

### Contrast threshold

The `contrastText` token is calculated using the `contrastThreshold` value, to maximize the contrast between the background and the text.

A higher contrast threshold value increases the point at which a background color is considered light, and thus given a dark `contrastText`.
Note that the contrast threshold follows a non-linear curve, and defaults to a value of 3 which indicates a minimum contrast ratio of 3:1.

```tsx
import { createTheme, ThemeProvider, useTheme } from '@mui/material/styles';
import Button from '@mui/material/Button';
import { Stack } from '@mui/system';

const defaultContrastThresholdTheme = createTheme({});

const highContrastThresholdTheme = createTheme({
  palette: {
    contrastThreshold: 4.5,
  },
});

function ContrastShowcase(props: { title: string }) {
  const { title } = props;
  const theme = useTheme();

  return (
    <Stack sx={{ gap: 1, alignItems: 'center' }}>
      <span>
        <b>{title}</b>
      </span>
      <span>{theme.palette.contrastThreshold}:1</span>
      <Stack direction="row" sx={{ gap: 1 }}>
        <Button variant="contained" color="warning">
          Warning
        </Button>
      </Stack>
    </Stack>
  );
}

export default function ContrastThreshold() {
  return (
    <Stack direction="row" sx={{ gap: 4 }}>
      <ThemeProvider theme={defaultContrastThresholdTheme}>
        <ContrastShowcase title="Default contrast threshold" />
      </ThemeProvider>
      <ThemeProvider theme={highContrastThresholdTheme}>
        <ContrastShowcase title="Higher contrast threshold" />
      </ThemeProvider>
    </Stack>
  );
}

```

### Tonal offset

The `light` and `dark` tokens are calculated using the `tonalOffset` value, to shift the `main` color's luminance.
A higher tonal offset value will make `light` tokens lighter, and `dark` tokens darker.

:::warning
This only applies when working with custom colors—it won't have any effect on the [default values](#default-colors).
:::

For example, the tonal offset default value `0.2` shifts the luminance by approximately two indexes, so if the `main` token is `blue[500]`, then the `light` token would be `blue[300]` and `dark` would be `blue[700]`.

The tonal offset value can be either a number between 0 and 1 (which would apply to both `light` and `dark` tokens) or an object with `light` and `dark` keys specified:

```tsx
import { createTheme, ThemeProvider, useTheme } from '@mui/material/styles';
import { blue } from '@mui/material/colors';
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';

const defaultTonalOffsetTheme = createTheme({
  palette: {
    primary: {
      main: blue[500],
    },
  },
});

const higherTonalOffsetTheme = createTheme({
  palette: {
    primary: {
      main: blue[500],
    },
    tonalOffset: 0.5,
  },
});

const asymmetricTonalOffsetTheme = createTheme({
  palette: {
    primary: {
      main: blue[500],
    },
    tonalOffset: {
      light: 0.1,
      dark: 0.9,
    },
  },
});

function ColorShowcase({ title, color }: { title: string; color: string }) {
  const {
    palette: { tonalOffset },
  } = useTheme();

  let caption;
  if (typeof tonalOffset === 'number') {
    caption = tonalOffset;
  } else {
    caption = `{ light: ${tonalOffset.light}, dark: ${tonalOffset.dark} }`;
  }

  return (
    <Stack sx={{ gap: 1, alignItems: 'center' }}>
      <span>
        <b>{title}</b>
      </span>
      <span>{caption}</span>
      <Stack direction="row" sx={{ gap: 1 }}>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">light</Typography>
          <Box sx={{ bgcolor: `${color}.light`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">main</Typography>
          <Box sx={{ bgcolor: `${color}.main`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">dark</Typography>
          <Box sx={{ bgcolor: `${color}.dark`, width: 40, height: 20 }} />
        </Stack>
      </Stack>
    </Stack>
  );
}

export default function TonalOffset() {
  return (
    <Stack direction={{ xs: 'column', sm: 'row' }} sx={{ gap: 8 }}>
      <ThemeProvider theme={defaultTonalOffsetTheme}>
        <ColorShowcase title="Default tonal offset" color="primary" />
      </ThemeProvider>
      <ThemeProvider theme={higherTonalOffsetTheme}>
        <ColorShowcase title="Higher tonal offset" color="primary" />
      </ThemeProvider>
      <ThemeProvider theme={asymmetricTonalOffsetTheme}>
        <ColorShowcase title="Asymmetric tonal offset" color="primary" />
      </ThemeProvider>
    </Stack>
  );
}

```

## Custom colors

:::warning
Unlike [default colors](#default-colors), tokens for custom colors are _not_ automatically calculated.
:::

To add custom colors, you must either provide the tokens manually, or generate them using the `augmentColor` utility:

### Provide tokens manually

The most straightforward approach is to define all tokens—`main`, `light`, `dark`, and `contrastText`—manually:

```jsx
import { createTheme } from '@mui/material/styles';

const theme = createTheme({
  palette: {
    ochre: {
      main: '#E3D026',
      light: '#E9DB5D',
      dark: '#A29415',
      contrastText: '#242105',
    },
  },
});
```

```tsx
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Button from '@mui/material/Button';
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';

// Augment the palette to include an ochre color
declare module '@mui/material/styles' {
  interface Palette {
    ochre: Palette['primary'];
  }

  interface PaletteOptions {
    ochre?: PaletteOptions['primary'];
  }
}

// Update the Button's color options to include an ochre option
declare module '@mui/material/Button' {
  interface ButtonPropsColorOverrides {
    ochre: true;
  }
}

const theme = createTheme({
  palette: {
    ochre: {
      main: '#E3D026',
      light: '#E9DB5D',
      dark: '#A29415',
      contrastText: '#242105',
    },
  },
});

export default function ManuallyProvideCustomColor() {
  return (
    <ThemeProvider theme={theme}>
      <Stack sx={{ gap: 2, alignItems: 'center' }}>
        <Button variant="contained" color="ochre">
          Ochre
        </Button>
        <Stack direction="row" sx={{ gap: 1 }}>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">light</Typography>
            <Box sx={{ bgcolor: 'ochre.light', width: 40, height: 20 }} />
          </Stack>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">main</Typography>
            <Box sx={{ bgcolor: 'ochre.main', width: 40, height: 20 }} />
          </Stack>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">dark</Typography>
            <Box sx={{ bgcolor: 'ochre.dark', width: 40, height: 20 }} />
          </Stack>
        </Stack>
      </Stack>
    </ThemeProvider>
  );
}

```

:::info
The supported color formats are `hex`, `rgb`, and `hsl`.
If you want to use other formats, such as `oklch`, you need to enable the [native color](/material-ui/customization/css-theme-variables/native-color/) feature.
:::

<!-- #target-branch-reference -->

If you need to manipulate colors, `@mui/material/styles` provides [a set of utilities](https://github.com/mui/material-ui/blob/v9.x/packages/mui-material/src/styles/index.d.ts#L52-L67) to help with this.
The following example uses the `alpha()` and `getContrastRatio()` utilities to define tokens using opacity:

```jsx
import { createTheme, alpha, getContrastRatio } from '@mui/material/styles';

const violetBase = '#7F00FF';
const violetMain = alpha(violetBase, 0.7);

const theme = createTheme({
  palette: {
    violet: {
      main: violetMain,
      light: alpha(violetBase, 0.5),
      dark: alpha(violetBase, 0.9),
      contrastText: getContrastRatio(violetMain, '#fff') > 4.5 ? '#fff' : '#111',
    },
  },
});
```

```tsx
import {
  createTheme,
  ThemeProvider,
  alpha,
  getContrastRatio,
} from '@mui/material/styles';
import Button from '@mui/material/Button';
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';

// Augment the palette to include a violet color
declare module '@mui/material/styles' {
  interface Palette {
    violet: Palette['primary'];
  }

  interface PaletteOptions {
    violet?: PaletteOptions['primary'];
  }
}

// Update the Button's color options to include a violet option
declare module '@mui/material/Button' {
  interface ButtonPropsColorOverrides {
    violet: true;
  }
}

const violetBase = '#7F00FF';
const violetMain = alpha(violetBase, 0.7);

const theme = createTheme({
  palette: {
    violet: {
      main: violetMain,
      light: alpha(violetBase, 0.5),
      dark: alpha(violetBase, 0.9),
      contrastText: getContrastRatio(violetMain, '#fff') > 4.5 ? '#fff' : '#111',
    },
  },
});

export default function UsingStylesUtils() {
  return (
    <ThemeProvider theme={theme}>
      <Stack sx={{ gap: 2, alignItems: 'center' }}>
        <Button variant="contained" color="violet">
          Violet
        </Button>
        <Stack direction="row" sx={{ gap: 1 }}>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">light</Typography>
            <Box sx={{ bgcolor: 'violet.light', width: 40, height: 20 }} />
          </Stack>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">main</Typography>
            <Box sx={{ bgcolor: 'violet.main', width: 40, height: 20 }} />
          </Stack>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">dark</Typography>
            <Box sx={{ bgcolor: 'violet.dark', width: 40, height: 20 }} />
          </Stack>
        </Stack>
      </Stack>
    </ThemeProvider>
  );
}

```

### Generate tokens using augmentColor utility

Alternatively, you can generate the `light`, `dark` and `contrastText` tokens using the palette's `augmentColor` utility, which is the same function used for the default palette colors.
This requires creating the theme in two steps and providing the `main` token on which the other will be based on:

```jsx
import { createTheme } from '@mui/material/styles';

let theme = createTheme({
  // Theme customization goes here as usual, including tonalOffset and/or
  // contrastThreshold as the augmentColor() function relies on these
});

theme = createTheme(theme, {
  // Custom colors created with augmentColor go here
  palette: {
    salmon: theme.palette.augmentColor({
      color: {
        main: '#FF5733',
      },
      name: 'salmon',
    }),
  },
});
```

```tsx
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Button from '@mui/material/Button';
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';

// Augment the palette to include a salmon color
declare module '@mui/material/styles' {
  interface Palette {
    salmon: Palette['primary'];
  }

  interface PaletteOptions {
    salmon?: PaletteOptions['primary'];
  }
}

// Update the Button's color options to include a salmon option
declare module '@mui/material/Button' {
  interface ButtonPropsColorOverrides {
    salmon: true;
  }
}

let theme = createTheme({
  // Theme customization goes here as usual, including tonalOffset and/or
  // contrastThreshold as the augmentColor() function relies on these
});

theme = createTheme(theme, {
  // Custom colors created with augmentColor go here
  palette: {
    salmon: theme.palette.augmentColor({
      color: {
        main: '#FF5733',
      },
      name: 'salmon',
    }),
  },
});

export default function UsingAugmentColor() {
  return (
    <ThemeProvider theme={theme}>
      <Stack sx={{ gap: 2, alignItems: 'center' }}>
        <Button variant="contained" color="salmon">
          Salmon
        </Button>
        <Stack direction="row" sx={{ gap: 1 }}>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">light</Typography>
            <Box sx={{ bgcolor: 'salmon.light', width: 40, height: 20 }} />
          </Stack>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">main</Typography>
            <Box sx={{ bgcolor: 'salmon.main', width: 40, height: 20 }} />
          </Stack>
          <Stack sx={{ alignItems: 'center' }}>
            <Typography variant="body2">dark</Typography>
            <Box sx={{ bgcolor: 'salmon.dark', width: 40, height: 20 }} />
          </Stack>
        </Stack>
      </Stack>
    </ThemeProvider>
  );
}

```

The [contrast threshold](#contrast-threshold) and [tonal offset](#tonal-offset) values will apply for the colors defined using this utility.

### Using in components

After adding a custom color, you will be able to use it in components just like you do with default palette colors:

```js
<Button color="custom">
```

### TypeScript

If you're using TypeScript, then you need to use [module augmentation](/material-ui/guides/typescript/#customization-of-theme) for custom colors.

To add a custom color to the palette, you must add it to the `Palette` and `PaletteOptions` interfaces:

<!-- tested with packages/mui-material/test/typescript/augmentation/paletteColors.spec.ts -->

```ts
declare module '@mui/material/styles' {
  interface Palette {
    custom: Palette['primary'];
  }

  interface PaletteOptions {
    custom?: PaletteOptions['primary'];
  }
}
```

To use a custom color for the `color` prop of a component, you must add it to the component's `PropsColorOverrides` interface.
The example below shows how to do this with a Button component:

<!-- tested with packages/mui-material/test/typescript/augmentation/paletteColors.spec.ts -->

```ts
declare module '@mui/material/Button' {
  interface ButtonPropsColorOverrides {
    custom: true;
  }
}
```

## Adding color tokens

To add a new [color token](#color-tokens), include it in the color's object as follows:

```jsx
import { createTheme } from '@mui/material/styles';
import { blue } from '@mui/material/colors';

const theme = createTheme({
  palette: {
    primary: {
      light: blue[300],
      main: blue[500],
      dark: blue[700],
      darker: blue[900],
    },
  },
});
```

```tsx
import { createTheme, ThemeProvider } from '@mui/material/styles';
import { blue } from '@mui/material/colors';
import Box from '@mui/material/Box';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';

declare module '@mui/material/styles' {
  interface PaletteColor {
    darker?: string;
  }

  interface SimplePaletteColorOptions {
    darker?: string;
  }
}

const theme = createTheme({
  palette: {
    primary: {
      light: blue[300],
      main: blue[500],
      dark: blue[700],
      darker: blue[900],
    },
  },
});

export default function AddingColorTokens() {
  return (
    <ThemeProvider theme={theme}>
      <Stack direction="row" sx={{ gap: 1 }}>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">light</Typography>
          <Box sx={{ bgcolor: `primary.light`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">main</Typography>
          <Box sx={{ bgcolor: `primary.main`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">dark</Typography>
          <Box sx={{ bgcolor: `primary.dark`, width: 40, height: 20 }} />
        </Stack>
        <Stack sx={{ alignItems: 'center' }}>
          <Typography variant="body2">darker</Typography>
          <Box sx={{ bgcolor: `primary.darker`, width: 40, height: 20 }} />
        </Stack>
      </Stack>
    </ThemeProvider>
  );
}

```

### TypeScript

If you're using TypeScript, then you'll need to use [module augmentation](/material-ui/guides/typescript/#customization-of-theme) to add the new color token to the `PaletteColor` and `SimplePaletteColorOptions` interfaces as follows:

<!-- tested with packages/mui-material/test/typescript/augmentation/paletteColors.spec.ts -->

```ts
declare module '@mui/material/styles' {
  interface PaletteColor {
    darker?: string;
  }

  interface SimplePaletteColorOptions {
    darker?: string;
  }
}
```

## Non-palette colors

To learn how to add colors outside of `theme.palette`, see [Theming—Custom variables](/material-ui/customization/theming/#custom-variables).

## Accessibility

To meet the minimum contrast of at least 4.5:1 as defined in [WCAG 2.2 Success Criterion 1.4.3](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html), create a custom theme with a [contrast threshold](#contrast-threshold) value of 4.5 as follows:

```js
import { createTheme } from '@mui/material/styles';

const theme = createTheme({
  palette: {
    contrastThreshold: 4.5,
  },
});
```

:::warning
The `contrastThreshold` parameter can produce counterproductive results.\
Please verify that the [APCA](https://contrast.tools/?tab=apca) color contrast is improved (WCAG 3 [will use](https://typefully.com/DanHollick/wcag-3-and-apca-sle13GMW2Brp) this new algorithm).
:::

### Windows High Contrast mode

To improve component visibility in Windows High Contrast mode (also known as [Forced Colors](https://developer.mozilla.org/en-US/docs/Web/CSS/@media/forced-colors)), use the `enhanceHighContrast` theme enhancer, starting from v9.1.0.
It accepts a fully-created theme and returns an enhanced version of it with `@media (forced-colors: active)` overrides applied to the affected components.

```js
import { createTheme, enhanceHighContrast } from '@mui/material/styles';

const theme = enhanceHighContrast(createTheme());
```

By default, it uses the [CSS system color keywords](https://www.w3.org/TR/css-color-4/#css-system-colors) (`Highlight`, `HighlightText`, `ButtonBorder`, etc.).
You can override individual tokens with other [system colors](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Values/system-color) to match your brand or design requirements.
When overriding paired tokens, choose values that the browser guarantees to contrast with each other — for example `SelectedItem` is always paired with `SelectedItemText`, and `Highlight` with `HighlightText`:

```js
import { createTheme, enhanceHighContrast } from '@mui/material/styles';

const theme = enhanceHighContrast(createTheme(), {
  // Use the system's selection colors for active or toggled controls.
  activeBackground: 'SelectedItem',
  activeText: 'SelectedItemText',
});
```

The following tokens are available, showing how your browser resolves each system color keyword in the current environment:

```jsx
import Box from '@mui/material/Box';
import Typography from '@mui/material/Typography';
import { styled } from '@mui/material/styles';

// TODO: Ideally this should be generated from the HighContrastTokens TS interface
// rather than maintained manually, but auto-extraction would lose descriptions and
// default values. A solution that exports token metadata from the interface is needed.
const tokens = [
  {
    token: 'disabled',
    color: 'GrayText',
    description: 'Color for disabled elements',
  },
  { token: 'error', color: 'ActiveText', description: 'Color for error states' },
  {
    token: 'selectedBackground',
    color: 'SelectedItem',
    description: 'Background color for selected items',
  },
  {
    token: 'selectedText',
    color: 'SelectedItemText',
    description: 'Text color on selected items',
  },
  {
    token: 'activeBackground',
    color: 'Highlight',
    description: 'Background color for active or toggled controls',
  },
  {
    token: 'activeText',
    color: 'HighlightText',
    description: 'Text color on active or toggled controls',
  },
  {
    token: 'buttonBorder',
    color: 'ButtonBorder',
    description: 'Border color for interactive controls',
  },
  {
    token: 'buttonText',
    color: 'ButtonText',
    description: 'Text and icon color on buttons',
  },
  {
    token: 'canvas',
    color: 'Canvas',
    description: 'Background color for the page or canvas',
  },
];

const Table = styled('table')(({ theme }) => ({
  width: '100%',
  borderCollapse: 'collapse',
  ...theme.typography.body2,
}));

const Th = styled('th')(({ theme }) => ({
  textAlign: 'left',
  padding: theme.spacing(1, 2),
  borderBottom: `2px solid ${theme.palette.divider}`,
  whiteSpace: 'nowrap',
}));

const Td = styled('td')(({ theme }) => ({
  padding: theme.spacing(1, 2),
  borderBottom: `1px solid ${theme.palette.divider}`,
  verticalAlign: 'middle',
}));

const ColorSwatch = styled('div')(({ theme }) => ({
  display: 'inline-block',
  width: theme.spacing(3),
  height: theme.spacing(3),
  marginRight: theme.spacing(1),
  borderRadius: theme.shape.borderRadius,
  outline: '1px solid ButtonBorder',
  verticalAlign: 'middle',
}));

export default function HighContrastTokens() {
  return (
    <Box sx={{ width: '100%', overflowX: 'auto' }}>
      <Table>
        <thead>
          <tr>
            <Th>Token</Th>
            <Th>Default</Th>
            <Th>Description</Th>
          </tr>
        </thead>
        <tbody>
          {tokens.map(({ token, color, description }) => (
            <tr key={token}>
              <Td>
                <code>{token}</code>
              </Td>
              <Td>
                <ColorSwatch style={{ backgroundColor: color }} />
                <Typography component="code" variant="body2">
                  {color}
                </Typography>
              </Td>
              <Td sx={{ color: 'text.secondary' }}>{description}</Td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Box>
  );
}

```

## Picking colors

Need inspiration? The Material Design team has built an [palette configuration tool](/material-ui/customization/color/#picking-colors) to help you.

## Color schemes

To add both built-in light and dark color schemes without creating separate themes, use the `colorSchemes: { light: true, dark: true }`.
This generates the default tokens for both color schemes:

```js
import { createTheme } from '@mui/material/styles';

const theme = createTheme({
  colorSchemes: {
    light: true,
    dark: true,
  },
});
```

To override the default tokens for each color scheme, use the same [palette object](#customization) as shown below:

```js
const theme = createTheme({
  colorSchemes: {
    light: {
      palette: {
        primary: {
          main: '#FF5733',
        },
        // ...other tokens
      },
    },
    dark: {
      palette: {
        primary: {
          main: '#E0C2FF',
        },
        // ...other tokens
      },
    },
  },
});
```

:::warning
The `colorSchemes` API is an enhanced version of the `palette` API, and is the preferred API for this purpose starting from Material UI v6.
If you provide both `colorSchemes` and `palette`, the latter will override any styles defined in the former.

```js
const theme = createTheme({
  palette: {
    primary: {
      main: '...',
    },
  },
  colorSchemes: {
    light: {
      // This will be replaced by the palette defined above
      palette: {
        primary: {
          main: '...',
        },
      },
    },
    dark: { ... },
  },
});
```

:::

## Dark mode

For details of how you can set up a dark mode for your theme, head to the [dark mode guide](/material-ui/customization/dark-mode/).
