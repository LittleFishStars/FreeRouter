# CSS theme variables - Native color

Learn how to use native color with CSS theme variables.

:::warning
This feature only works in modern browsers. Please check the [browser support](https://caniuse.com/css-relative-colors) before using it.
:::

## Benefits

- No need to use JavaScript to manipulate colors.
- Supports modern color spaces such as `oklch`, `oklab`, and `display-p3`.
- Supports color aliases to external CSS variables.
- Automatically calculates contrast text from the main color.

## Usage

Set `cssVariables` with `nativeColor: true` in the theme options.
Material UI will start using CSS color-mix and relative color instead of the JavaScript color manipulation.

:::success
Try inspecting the demo below to see the calculated values of the color tokens.
:::

```js
const theme = createTheme({
  cssVariables: {
    nativeColor: true,
  },
});
```

```tsx
import { ThemeProvider, createTheme } from '@mui/material/styles';
import Card from '@mui/material/Card';
import CardContent from '@mui/material/CardContent';
import CardActions from '@mui/material/CardActions';
import Alert from '@mui/material/Alert';
import Button from '@mui/material/Button';

const theme = createTheme({
  cssVariables: {
    nativeColor: true,
    cssVarPrefix: 'nativeColor', // This is for the demo only, you don't need to set this to use the feature
    colorSchemeSelector: 'data-mui-color-scheme',
  },
  colorSchemes: {
    light: true,
    dark: true,
  },
});

export default function NativeCssColors() {
  return (
    <ThemeProvider theme={theme}>
      <Card>
        <CardContent>
          <Alert severity="info">
            This theme uses the <code>oklch</code> color space.
          </Alert>
        </CardContent>
        <CardActions sx={{ justifyContent: 'flex-end' }}>
          <Button variant="contained" color="primary">
            Submit
          </Button>
          <Button variant="outlined" color="primary">
            Cancel
          </Button>
        </CardActions>
      </Card>
    </ThemeProvider>
  );
}

```

## Modern color spaces

The theme palette supports all modern color spaces, including `oklch`, `oklab`, and `display-p3`.

```js
const theme = createTheme({
  cssVariables: { nativeColor: true },
  palette: {
    primary: {
      main: 'color(display-p3 0.5 0.8 0.2)',
    },
  },
});
```

```tsx
import * as React from 'react';
import { ThemeProvider, createTheme } from '@mui/material/styles';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Radio from '@mui/material/Radio';
import RadioGroup from '@mui/material/RadioGroup';
import FormLabel from '@mui/material/FormLabel';
import FormControlLabel from '@mui/material/FormControlLabel';
import FormControl from '@mui/material/FormControl';

export default function ModernColorSpaces() {
  const colorSpaces = [
    'color(display-p3 0.7 0.5 0)', // Mud
    'oklch(0.62 0.25 29)', // Orange
    'oklab(0.59 0.1 -0.14)', // Purple
    'hsl(141, 70%, 48%)', // Green
    'rgb(25, 118, 210)', // Blue
  ];

  const [selectedColor, setSelectedColor] = React.useState(colorSpaces[0]);

  const theme = React.useMemo(
    () =>
      createTheme({
        cssVariables: {
          nativeColor: true,
          cssVarPrefix: 'modern-color-spaces',
        },
        palette: {
          primary: {
            main: selectedColor,
          },
        },
      }),
    [selectedColor],
  );

  return (
    <Box sx={{ display: 'flex', gap: 3, alignItems: 'center', flexWrap: 'wrap' }}>
      <FormControl>
        <FormLabel>Main color</FormLabel>
        <RadioGroup
          value={selectedColor}
          onChange={(event) => setSelectedColor(event.target.value)}
        >
          {colorSpaces.map((value) => (
            <FormControlLabel
              key={value}
              value={value}
              control={<Radio />}
              label={value}
            />
          ))}
        </RadioGroup>
      </FormControl>

      <ThemeProvider theme={theme}>
        <Button variant="contained" size="large">
          Button
        </Button>
      </ThemeProvider>
    </Box>
  );
}

```

## Aliasing color variables

If you're using CSS variables to define colors, you can provide the values to the theme palette options.

```js
const theme = createTheme({
  cssVariables: {
    nativeColor: true,
  },
  palette: {
    primary: {
      main: 'var(--colors-brand-primary)',
    },
  },
});
```

```tsx
import { ThemeProvider, createTheme } from '@mui/material/styles';
import GlobalStyles from '@mui/material/GlobalStyles';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';

const theme = createTheme({
  cssVariables: {
    nativeColor: true,
    cssVarPrefix: 'alias', // This is for the demo only, you don't need to set this to use the feature
  },
  palette: {
    primary: {
      main: 'var(--colors-brand-primary)',
    },
  },
});

export default function AliasColorVariables() {
  return (
    <div>
      {/* This is just a demo to replicate the global CSS file */}
      <GlobalStyles
        styles={{
          ':root': {
            '--colors-brand-primary': 'oklch(0.85 0.2 83.89)',
          },
        }}
      />

      {/* Your App */}
      <ThemeProvider theme={theme}>
        <Box sx={{ p: 2 }}>
          <Button variant="contained">Branded Button</Button>
        </Box>
      </ThemeProvider>
    </div>
  );
}

```

## Theme color functions

The theme object contains these color utilities: `alpha()`, `lighten()`, and `darken()`.

When native color is enabled, these functions use CSS `color-mix()` and relative color instead of the JavaScript color manipulation.

```tsx
import * as React from 'react';
import { ThemeProvider, createTheme } from '@mui/material/styles';
import { blue, purple, red, green, orange, brown } from '@mui/material/colors';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Typography from '@mui/material/Typography';

const theme = createTheme({
  cssVariables: {
    nativeColor: true,
    // This is for the demo only, you don't need to set this to use the feature
    cssVarPrefix: 'demo',
    colorSchemeSelector: 'data',
  },
  colorSchemes: {
    light: true,
    dark: true,
  },
});

const colorSwatches = [
  { color: blue[500] },
  { color: purple[500] },
  { color: red[500] },
  { color: brown[600] },
  { color: green[600] },
  { color: orange[500] },
];

function ColorDisplay({ color }: { color: string }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
      <Box
        sx={{
          width: 48,
          height: 48,
          bgcolor: color,
          borderRadius: 1,
          border: '1px solid',
          borderColor: 'divider',
        }}
      />
      <Typography
        variant="caption"
        sx={{
          color: 'text.secondary',
          fontFamily: 'monospace',
          wordBreak: 'break-all',
        }}
      >
        {color}
      </Typography>
    </Box>
  );
}

export default function ThemeColorFunctions() {
  const [selectedColor, setSelectedColor] = React.useState(colorSwatches[0]);

  const colorValues = {
    alpha: theme.alpha(selectedColor.color, 0.5),
    lighten: theme.lighten(selectedColor.color, 0.5),
    darken: theme.darken(selectedColor.color, 0.5),
  };

  return (
    <ThemeProvider theme={theme}>
      <Box sx={{ p: 2 }}>
        <Box sx={{ display: 'flex', gap: 1, mb: 3, flexWrap: 'wrap' }}>
          {colorSwatches.map((swatch) => {
            const isSelected = selectedColor.color === swatch.color;
            return (
              <Button
                key={swatch.color}
                variant={isSelected ? 'contained' : 'outlined'}
                onClick={() => setSelectedColor(swatch)}
                sx={(t) => ({
                  width: 56,
                  height: 56,
                  minWidth: 56,
                  p: 0,
                  fontSize: '0.625rem',
                  fontFamily: 'monospace',
                  borderColor: isSelected ? 'transparent' : swatch.color,
                  bgcolor: isSelected ? swatch.color : 'transparent',
                  color: isSelected
                    ? t.palette.getContrastText(swatch.color)
                    : swatch.color,
                })}
              >
                {swatch.color}
              </Button>
            );
          })}
        </Box>
        <Box
          sx={{
            display: 'grid',
            gap: 2,
            gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))',
          }}
        >
          <div>
            <Typography
              variant="subtitle2"
              gutterBottom
              sx={{ fontWeight: 'medium' }}
            >
              theme.alpha(color, 0.5)
            </Typography>
            <ColorDisplay color={colorValues.alpha} />
          </div>
          <div>
            <Typography
              variant="subtitle2"
              gutterBottom
              sx={{ fontWeight: 'medium' }}
            >
              theme.lighten(color, 0.5)
            </Typography>
            <ColorDisplay color={colorValues.lighten} />
          </div>
          <div>
            <Typography
              variant="subtitle2"
              gutterBottom
              sx={{ fontWeight: 'medium' }}
            >
              theme.darken(color, 0.5)
            </Typography>
            <ColorDisplay color={colorValues.darken} />
          </div>
        </Box>
      </Box>
    </ThemeProvider>
  );
}

```

:::info
The theme color functions are backward compatible.
If native color is not enabled, they will fall back to the JavaScript color manipulation.
:::

## Contrast color function

The `theme.palette.getContrastText()` function produces the contrast color.
The demo below shows the result of the `theme.palette.getContrastText()` function, which produces the text color based on the selected background.

```tsx
import * as React from 'react';
import { ThemeProvider, createTheme } from '@mui/material/styles';
import Box from '@mui/material/Box';
import Typography from '@mui/material/Typography';
import Slider from '@mui/material/Slider';

const theme = createTheme({
  cssVariables: {
    nativeColor: true,
    cssVarPrefix: 'contrast', // This is for the demo only, you don't need to set this to use the feature
  },
});

export default function ContrastTextDemo() {
  const [lightness, setLightness] = React.useState(0.65);
  const [chroma, setChroma] = React.useState(0.3);
  const [hue, setHue] = React.useState(29);

  // Create OKLCH color from slider values
  const backgroundColor = `oklch(${lightness} ${chroma} ${hue})`;

  // Get contrast text using theme function
  const contrastText = theme.palette.getContrastText(backgroundColor);

  return (
    <ThemeProvider theme={theme}>
      <Box
        sx={{
          p: 2,
          display: 'flex',
          gap: 5,
          alignItems: 'flex-start',
          justifyContent: 'center',
          width: '100%',
          flexWrap: 'wrap',
        }}
      >
        {/* Live Preview Square */}
        <Box
          sx={{
            mt: 2,
            width: 200,
            height: 200,
            bgcolor: backgroundColor,
            color: contrastText,
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'center',
            alignItems: 'center',
            textAlign: 'center',
            borderRadius: 1,
            border: '1px solid',
            borderColor: 'divider',
            flexShrink: 0,
          }}
        >
          <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
            {backgroundColor}
          </Typography>
        </Box>
        {/* Sliders */}
        <Box sx={{ flex: '1 1 300px', maxWidth: 400 }}>
          <Typography variant="h6" gutterBottom>
            OKLCH
          </Typography>
          <div>
            <Typography variant="body2" gutterBottom>
              Lightness: {lightness}
            </Typography>
            <Slider
              value={lightness}
              onChange={(_, value) => setLightness(value)}
              min={0}
              max={1}
              step={0.01}
              valueLabelDisplay="auto"
            />
          </div>

          <div>
            <Typography variant="body2" gutterBottom>
              Chroma: {chroma}
            </Typography>
            <Slider
              value={chroma}
              onChange={(_, value) => setChroma(value)}
              min={0}
              max={0.4}
              step={0.01}
              valueLabelDisplay="auto"
            />
          </div>

          <div>
            <Typography variant="body2" gutterBottom>
              Hue: {hue}°
            </Typography>
            <Slider
              value={hue}
              onChange={(_, value) => setHue(value)}
              min={0}
              max={360}
              step={1}
              valueLabelDisplay="auto"
            />
          </div>
        </Box>
        <Box
          sx={{
            p: 2,
            display: 'flex',
            gap: 3,
          }}
        >
          <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
            <b>Text color:</b> {contrastText}
          </Typography>
        </Box>
      </Box>
    </ThemeProvider>
  );
}

```

:::info
The CSS variables `--__l` and `--__a` are internal variables set globally by Material UI.

To learn more about the formulas used, see [this article on color contrast from Lea Verou](https://lea.verou.me/blog/2024/contrast-color/).
:::

## Caveats

- Because of the differences in how contrast is calculated between CSS and JavaScript, the resulting CSS colors may not exactly match the corresponding JavaScript colors to be replaced.
- In the future, the relative color contrast will be replaced by the native [CSS `contrast-color()` function](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Values/color_value/contrast-color) when browser support is improved.
- For relative color contrast, the color space is automatically set to `oklch` internally. Currently it's not possible to change this, but please [open an issue](https://github.com/mui/material-ui/issues/new/) if you have a use case that calls for it.
