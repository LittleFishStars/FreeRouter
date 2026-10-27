# Focus visible

Apply an outline or inset keyboard focus across Material UI components.

Starting from v9.4, Material UI provides built-in support for visual keyboard focus indicator through CSS. The demos on this page opt out of the ripple to show only the focus visible indicator.

## Usage

Set `focusVisible: true` on the theme to render a default focus indicator on every [ButtonBase](/material-ui/api/button-base/)-derived component when it receives **keyboard** focus:

```js
import { createTheme } from '@mui/material/styles';

const theme = createTheme({ focusVisible: true });
```

The default focus indicator is a two-pixel solid outline with `palette.primary.main` color, offset by two pixels:

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Stack from '@mui/material/Stack';
import Button from '@mui/material/Button';
import IconButton from '@mui/material/IconButton';
import Typography from '@mui/material/Typography';
import DeleteIcon from '@mui/icons-material/Delete';

const theme = createTheme({
  focusVisible: true,
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

export default function FocusVisibleDefault() {
  return (
    <ThemeProvider theme={theme}>
      <Stack spacing={2} sx={{ alignItems: 'center' }}>
        <Typography variant="body2" color="text.secondary">
          Press <kbd>Tab</kbd> to move keyboard focus — the ring appears on focus.
        </Typography>
        <Stack direction="row" spacing={2} sx={{ alignItems: 'center' }}>
          <Button variant="outlined">Outlined</Button>
          <IconButton aria-label="delete">
            <DeleteIcon />
          </IconButton>
        </Stack>
      </Stack>
    </ThemeProvider>
  );
}

```

:::info

Why an outline

CSS `outline` is the most common indicator found in the web standard that works in most environment including high-contrast color mode.
:::

### Inner focus indicator

Some components, for example `Tab`, render the focus indicator from the inside to avoid `overflow`-clipped container or overlapping with other elements.

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Stack from '@mui/material/Stack';
import Tabs from '@mui/material/Tabs';
import Tab from '@mui/material/Tab';
import Typography from '@mui/material/Typography';

const theme = createTheme({
  focusVisible: true,
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

export default function FocusVisibleInner() {
  const [value, setValue] = React.useState(0);
  const handleChange = (event: React.SyntheticEvent, newValue: number) => {
    setValue(newValue);
  };
  return (
    <ThemeProvider theme={theme}>
      <Stack spacing={2} sx={{ alignItems: 'center' }}>
        <Typography variant="body2" color="text.secondary">
          Press <kbd>Tab</kbd>, then use the arrow keys — the ring insets so the Tabs
          scroller cannot clip it.
        </Typography>
        <Tabs value={value} onChange={handleChange}>
          <Tab label="One" />
          <Tab label="Two" />
          <Tab label="Three" />
        </Tabs>
      </Stack>
    </ThemeProvider>
  );
}

```

To see the full list of components that show inner focus indicator, check out the [full demo](#full-focus-visible-demo) below.

### Colored surface container

Components that support keyboard focus visible will show another layer of box-shadow indicator when they render within `AppBar`, `Alert`, and `SnackbarContent`. This comes by default when the focus visible feature is enabled, unless a custom box-shadow is provided.

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Stack from '@mui/material/Stack';
import AppBar from '@mui/material/AppBar';
import Toolbar from '@mui/material/Toolbar';
import Typography from '@mui/material/Typography';
import Alert from '@mui/material/Alert';
import SnackbarContent from '@mui/material/SnackbarContent';
import Button from '@mui/material/Button';
import IconButton from '@mui/material/IconButton';
import MenuIcon from '@mui/icons-material/Menu';
import AddIcon from '@mui/icons-material/Add';
import CloseIcon from '@mui/icons-material/Close';

const theme = createTheme({
  focusVisible: true,
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

export default function FocusVisibleColoredSurface() {
  return (
    <ThemeProvider theme={theme}>
      <Stack spacing={3} sx={{ alignItems: 'center' }}>
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ alignSelf: 'center' }}
        >
          Press <kbd>Tab</kbd> — a background-colored box-shadow renders behind the
          outline so the ring stays visible on the colored surface.
        </Typography>
        <AppBar position="static" sx={{ borderRadius: 1 }}>
          <Toolbar>
            <IconButton
              edge="start"
              color="inherit"
              aria-label="menu"
              sx={{ mr: 2 }}
            >
              <MenuIcon />
            </IconButton>
            <Typography variant="h6" component="div" sx={{ flexGrow: 1 }}>
              Title
            </Typography>
            <Button color="inherit">Login</Button>
            <IconButton color="inherit" aria-label="add">
              <AddIcon />
            </IconButton>
          </Toolbar>
        </AppBar>
        <Alert
          variant="filled"
          severity="error"
          action={
            <React.Fragment>
              <Button color="inherit" size="small">
                UNDO
              </Button>
              <IconButton color="inherit" size="small" aria-label="close">
                <CloseIcon fontSize="inherit" />
              </IconButton>
            </React.Fragment>
          }
        >
          Something went wrong
        </Alert>
        <SnackbarContent
          message="Message sent"
          action={
            <Button color="inherit" size="small">
              UNDO
            </Button>
          }
        />
      </Stack>
    </ThemeProvider>
  );
}

```

## Customization

The `focusVisible` can be customized by passing a CSS object to merge with the default styles.

### Changing the outline color

To customize the outline, for example changing the color, pass an object with specified outline color to the `focusVisible` node to merge with the default outline styles:

```js
// Recolor only; width and offset stay at the curated 2px.
createTheme({ focusVisible: { outlineColor: '#9c27b0' } });
```

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Button from '@mui/material/Button';

// Merge over the curated default: recolor only, width and offset stay at 2px.
const theme = createTheme({
  focusVisible: { outlineColor: '#9c27b0' },
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

export default function FocusVisibleRecolor() {
  return (
    <ThemeProvider theme={theme}>
      <Button variant="outlined">Tab to me</Button>
    </ThemeProvider>
  );
}

```

### Use box-shadow as a second layer

A `boxShadow` can be **additive** on top of the outline. This is useful for a two-color ring (WCAG technique [C40](https://www.w3.org/WAI/WCAG21/Techniques/css/C40)) that stays visible on any background. Material UI insets the box-shadow automatically on the inner focus indicator components, so a plain single-layer value works everywhere:

```js
createTheme({
  focusVisible: {
    /* inner indicator */
    outlineColor: '#F9F9F9',
    outlineOffset: 0,
    /* outer indicator */
    boxShadow: '0 0 0 4px #193146',
  },
});
```

:::info
Components with their own focus box-shadow compose both layers — for example, the Button and Fab keep their focus elevation and render the box-shadow above together with it.
:::

:::warning
Use a single box-shadow layer. A comma-separated value is not supported: only the first layer is inset on the inner focus indicator components, so every layer after it stays outset and is clipped away.

For a two-color ring, stack `outlineColor` and `boxShadow` as shown above rather than stacking two box-shadow layers.
:::

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Stack from '@mui/material/Stack';
import Card from '@mui/material/Card';
import AppBar from '@mui/material/AppBar';
import Toolbar from '@mui/material/Toolbar';
import Typography from '@mui/material/Typography';
import Button from '@mui/material/Button';
import Tabs from '@mui/material/Tabs';
import Tab from '@mui/material/Tab';

const theme = createTheme({
  focusVisible: {
    /* inner indicator */
    outlineColor: '#193146',
    outlineOffset: 2,
    /* outer indicator */
    boxShadow: '0 0 0 4px #FFF',
  },
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

export default function FocusVisibleBoxShadow() {
  const [cardTab, setCardTab] = React.useState(0);
  const [appBarTab, setAppBarTab] = React.useState(0);
  const handleCardChange = (event: React.SyntheticEvent, newValue: number) => {
    setCardTab(newValue);
  };
  const handleAppBarChange = (event: React.SyntheticEvent, newValue: number) => {
    setAppBarTab(newValue);
  };
  return (
    <ThemeProvider theme={theme}>
      <Stack spacing={2} sx={{ width: '100%' }}>
        <Typography variant="body2" color="text.secondary">
          Press <kbd>Tab</kbd> — the light outline or the dark box-shadow keeps
          contrast on either background.
        </Typography>
        <Card
          sx={{
            px: 3,
            minHeight: 64,
            display: 'flex',
            alignItems: 'center',
            gap: 3,
          }}
        >
          <Button variant="outlined">Tab to me</Button>
          <Tabs value={cardTab} onChange={handleCardChange}>
            <Tab label="Tab one" />
            <Tab label="Tab two" />
          </Tabs>
        </Card>
        <AppBar position="static" sx={{ borderRadius: 1 }}>
          <Toolbar sx={{ gap: 3 }}>
            <Button variant="outlined" color="inherit">
              Tab to me
            </Button>
            <Tabs
              value={appBarTab}
              onChange={handleAppBarChange}
              textColor="inherit"
              indicatorColor="secondary"
            >
              <Tab label="Tab one" />
              <Tab label="Tab two" />
            </Tabs>
          </Toolbar>
        </AppBar>
      </Stack>
    </ThemeProvider>
  );
}

```

### Replace outline with box-shadow

To replace the outline entirely with a box-shadow indicator, hide the outline with `outlineColor: 'transparent'`:

```js
createTheme({
  focusVisible: {
    outlineColor: 'transparent',
    boxShadow: '0 0 0 3px #1976d2',
  },
});
```

:::success
Hide the outline with `outlineColor: 'transparent'`, not `outline: 'none'`. In [forced-colors mode](https://developer.mozilla.org/en-US/docs/Web/CSS/@media/forced-colors) the browser strips the box-shadow and forces the outline to a system color, so a transparent outline reappears as the indicator. `outline: 'none'` removes it, leaving no keyboard focus indicator at all.
:::

## Full focus visible demo

The complete set of components that render the focus indicator when `focusVisible` is enabled. Use the keyboard (<kbd>Tab</kbd> and arrow keys) to move focus and reveal the ring.

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import Box from '@mui/material/Box';
import Divider from '@mui/material/Divider';
import Stack from '@mui/material/Stack';
import Paper from '@mui/material/Paper';
import Typography from '@mui/material/Typography';
import Button from '@mui/material/Button';
import IconButton from '@mui/material/IconButton';
import ButtonGroup from '@mui/material/ButtonGroup';
import ToggleButton from '@mui/material/ToggleButton';
import ToggleButtonGroup from '@mui/material/ToggleButtonGroup';
import Fab from '@mui/material/Fab';
import Chip from '@mui/material/Chip';
import FormGroup from '@mui/material/FormGroup';
import FormControlLabel from '@mui/material/FormControlLabel';
import Checkbox from '@mui/material/Checkbox';
import RadioGroup from '@mui/material/RadioGroup';
import Radio from '@mui/material/Radio';
import Switch from '@mui/material/Switch';
import Stepper from '@mui/material/Stepper';
import Step from '@mui/material/Step';
import StepButton from '@mui/material/StepButton';
import Pagination from '@mui/material/Pagination';
import ButtonBase from '@mui/material/ButtonBase';
import Accordion from '@mui/material/Accordion';
import AccordionSummary from '@mui/material/AccordionSummary';
import AccordionDetails from '@mui/material/AccordionDetails';
import Table from '@mui/material/Table';
import TableContainer from '@mui/material/TableContainer';
import TableHead from '@mui/material/TableHead';
import TableBody from '@mui/material/TableBody';
import TableRow from '@mui/material/TableRow';
import TableCell from '@mui/material/TableCell';
import TableSortLabel from '@mui/material/TableSortLabel';
import Slider from '@mui/material/Slider';
import Link from '@mui/material/Link';
import Breadcrumbs from '@mui/material/Breadcrumbs';
import Rating from '@mui/material/Rating';
import Tabs from '@mui/material/Tabs';
import Tab from '@mui/material/Tab';
import MenuList from '@mui/material/MenuList';
import MenuItem from '@mui/material/MenuItem';
import List from '@mui/material/List';
import ListItemButton from '@mui/material/ListItemButton';
import BottomNavigation from '@mui/material/BottomNavigation';
import BottomNavigationAction from '@mui/material/BottomNavigationAction';
import Card from '@mui/material/Card';
import CardActionArea from '@mui/material/CardActionArea';
import FormControl from '@mui/material/FormControl';
import InputLabel from '@mui/material/InputLabel';
import Select from '@mui/material/Select';
import Autocomplete from '@mui/material/Autocomplete';
import TextField from '@mui/material/TextField';
import Dialog from '@mui/material/Dialog';
import DialogTitle from '@mui/material/DialogTitle';
import DialogContent from '@mui/material/DialogContent';
import DialogContentText from '@mui/material/DialogContentText';
import DialogActions from '@mui/material/DialogActions';
import AddIcon from '@mui/icons-material/Add';
import StarIcon from '@mui/icons-material/Star';
import HomeIcon from '@mui/icons-material/Home';

const theme = createTheme({
  focusVisible: true,
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

const noop = () => {};

const dialogParagraphs = Array.from(
  { length: 40 },
  (_, index) =>
    `Paragraph ${index + 1}. Cras mattis consectetur purus sit amet fermentum. Cras justo odio, dapibus ac facilisis in, egestas eget quam.`,
);

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <React.Fragment>
      <Typography variant="body2" sx={{ fontWeight: 600, alignSelf: 'center' }}>
        {label}
      </Typography>
      <Stack
        direction="row"
        spacing={1.5}
        sx={{ alignItems: 'center', flexWrap: 'wrap', rowGap: 1 }}
      >
        {children}
      </Stack>
    </React.Fragment>
  );
}

function Bucket({
  title,
  hint,
  children,
}: {
  title: string;
  hint: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <Typography variant="overline" sx={{ fontWeight: 700 }}>
        {title}
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ display: 'block', mb: 1.5 }}
      >
        {hint}
      </Typography>
      <Box
        sx={{
          display: 'grid',
          gridTemplateColumns: '150px 1fr',
          alignItems: 'center',
          columnGap: 3,
          rowGap: 2,
        }}
      >
        {children}
      </Box>
    </div>
  );
}

export default function FullFocusVisibleDemo() {
  const [tab, setTab] = React.useState(0);
  const handleTabChange = (event: React.SyntheticEvent, newValue: number) => {
    setTab(newValue);
  };
  const [alignment, setAlignment] = React.useState<string | null>('left');
  const handleAlignmentChange = (
    event: React.MouseEvent<HTMLElement>,
    newAlignment: string | null,
  ) => {
    setAlignment(newAlignment);
  };
  const [orderBy, setOrderBy] = React.useState('name');
  const [order, setOrder] = React.useState<'asc' | 'desc'>('asc');
  const createSortHandler = (column: string) => () => {
    if (orderBy === column) {
      setOrder(order === 'asc' ? 'desc' : 'asc');
    } else {
      setOrderBy(column);
      setOrder('asc');
    }
  };
  const [activeStep, setActiveStep] = React.useState(0);
  const handleStep = (step: number) => () => {
    setActiveStep(step);
  };
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const handleDialogOpen = () => {
    setDialogOpen(true);
  };
  const handleDialogClose = () => {
    setDialogOpen(false);
  };
  return (
    <ThemeProvider theme={theme}>
      <Stack spacing={3}>
        <Bucket
          title="outer-ring"
          hint="The ring renders fully outside the component."
        >
          <Row label="Button">
            <Button variant="text">Text</Button>
            <Button variant="outlined">Outlined</Button>
            <Button variant="contained">Contained</Button>
          </Row>
          <Row label="IconButton">
            <IconButton aria-label="star">
              <StarIcon />
            </IconButton>
          </Row>
          <Row label="ButtonGroup">
            <ButtonGroup variant="outlined">
              <Button>One</Button>
              <Button>Two</Button>
            </ButtonGroup>
          </Row>
          <Row label="ToggleButton">
            <ToggleButtonGroup
              value={alignment}
              onChange={handleAlignmentChange}
              exclusive
            >
              <ToggleButton value="left">Left</ToggleButton>
              <ToggleButton value="right">Right</ToggleButton>
            </ToggleButtonGroup>
          </Row>
          <Row label="Fab">
            <Fab size="small" color="primary" aria-label="add">
              <AddIcon />
            </Fab>
          </Row>
          <Row label="Chip">
            <Chip label="Clickable" onClick={noop} />
            <Chip label="Deletable" onDelete={noop} />
          </Row>
          <Row label="Checkbox">
            <FormGroup>
              <FormControlLabel
                control={<Checkbox defaultChecked />}
                label="Checkbox A"
              />
              <FormControlLabel control={<Checkbox />} label="Checkbox B" />
            </FormGroup>
          </Row>
          <Row label="Radio">
            <RadioGroup defaultValue="a">
              <FormControlLabel value="a" control={<Radio />} label="Radio A" />
              <FormControlLabel value="b" control={<Radio />} label="Radio B" />
            </RadioGroup>
          </Row>
          <Row label="Switch">
            <FormControlLabel control={<Switch defaultChecked />} label="Switch" />
          </Row>
          <Row label="Pagination">
            <Pagination count={3} />
          </Row>
          <Row label="ButtonBase">
            <ButtonBase
              sx={{
                px: 1.5,
                py: 1,
                border: '1px dashed',
                borderColor: 'divider',
                borderRadius: 1,
              }}
            >
              ButtonBase
            </ButtonBase>
          </Row>
          <Row label="AccordionSummary">
            <Accordion disableGutters sx={{ width: 280 }}>
              <AccordionSummary>Accordion header</AccordionSummary>
              <AccordionDetails>
                <Typography variant="body2">Details</Typography>
              </AccordionDetails>
            </Accordion>
          </Row>
          <Row label="TableSortLabel">
            <TableContainer component={Paper} variant="outlined" sx={{ width: 280 }}>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>
                      <TableSortLabel
                        active={orderBy === 'name'}
                        direction={orderBy === 'name' ? order : 'asc'}
                        onClick={createSortHandler('name')}
                      >
                        Name
                      </TableSortLabel>
                    </TableCell>
                    <TableCell>
                      <TableSortLabel
                        active={orderBy === 'size'}
                        direction={orderBy === 'size' ? order : 'asc'}
                        onClick={createSortHandler('size')}
                      >
                        Size
                      </TableSortLabel>
                    </TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  <TableRow>
                    <TableCell>file.txt</TableCell>
                    <TableCell>12 KB</TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </TableContainer>
          </Row>
          <Row label="Slider">
            <Slider defaultValue={40} aria-label="Volume" sx={{ width: 200 }} />
          </Row>
          <Row label="Link">
            <Link href="#">Text link</Link>
          </Row>
          <Row label="Breadcrumbs">
            <Breadcrumbs>
              <Link href="#">Home</Link>
              <Link href="#">Catalog</Link>
              <Typography color="text.primary">Item</Typography>
            </Breadcrumbs>
          </Row>
          <Row label="Rating">
            <Rating defaultValue={3} />
          </Row>
          <Row label="Stepper">
            <Stepper nonLinear activeStep={activeStep} sx={{ minWidth: 260 }}>
              <Step>
                <StepButton onClick={handleStep(0)}>One</StepButton>
              </Step>
              <Step>
                <StepButton onClick={handleStep(1)}>Two</StepButton>
              </Step>
            </Stepper>
          </Row>
          <Row label="Dialog">
            <Button variant="outlined" onClick={handleDialogOpen}>
              Open dialog
            </Button>
            <Dialog
              open={dialogOpen}
              onClose={handleDialogClose}
              scroll="paper"
              aria-labelledby="fv-dialog-title"
            >
              <DialogTitle id="fv-dialog-title">Subscribe</DialogTitle>
              <DialogContent dividers>
                {dialogParagraphs.map((paragraph) => (
                  <DialogContentText key={paragraph} sx={{ mb: 1 }}>
                    {paragraph}
                  </DialogContentText>
                ))}
              </DialogContent>
              <DialogActions>
                <Button onClick={handleDialogClose}>Cancel</Button>
                <Button onClick={handleDialogClose}>Subscribe</Button>
              </DialogActions>
            </Dialog>
          </Row>
        </Bucket>

        <Divider />

        <Bucket
          title="inner-ring"
          hint="Inside a scrollable or overflow-clipped container — the ring is inset (outlineOffset -2) so it cannot be clipped."
        >
          <Row label="Tab">
            <Tabs value={tab} onChange={handleTabChange} sx={{ minHeight: 0 }}>
              <Tab label="Tab one" />
              <Tab label="Tab two" />
            </Tabs>
          </Row>
          <Row label="MenuItem">
            <MenuList>
              <MenuItem>Profile</MenuItem>
              <MenuItem>Settings</MenuItem>
            </MenuList>
          </Row>
          <Row label="ListItemButton">
            <List>
              <ListItemButton>List item button 1</ListItemButton>
              <ListItemButton>List item button 2</ListItemButton>
            </List>
          </Row>
          <Row label="BottomNavigation">
            <BottomNavigation showLabels value={0} sx={{ width: 320 }}>
              <BottomNavigationAction label="Star" icon={<StarIcon />} />
              <BottomNavigationAction label="Home" icon={<HomeIcon />} />
              <BottomNavigationAction label="Add" icon={<AddIcon />} />
            </BottomNavigation>
          </Row>
          <Row label="CardActionArea">
            <Card variant="outlined" sx={{ width: 160 }}>
              <CardActionArea>
                <Box sx={{ p: 2 }}>
                  <Typography variant="body2">Card</Typography>
                </Box>
              </CardActionArea>
            </Card>
          </Row>
          <Row label="Select">
            <FormControl size="small" sx={{ minWidth: 160 }}>
              <InputLabel id="fv-select-label">Select</InputLabel>
              <Select labelId="fv-select-label" label="Select" defaultValue="a">
                <MenuItem value="a">Option A</MenuItem>
                <MenuItem value="b">Option B</MenuItem>
                <MenuItem value="c">Option C</MenuItem>
              </Select>
            </FormControl>
          </Row>
          <Row label="Autocomplete">
            <Autocomplete
              options={['Apple', 'Banana', 'Cherry']}
              sx={{ width: 220 }}
              renderInput={(params) => (
                <TextField {...params} label="Autocomplete" size="small" />
              )}
            />
          </Row>
        </Bucket>
      </Stack>
    </ThemeProvider>
  );
}

```

## Caveats

### Checkbox and Radio custom icons must be SVG

The Checkbox and Radio attach the focus indicator to the first `<svg>` element inside the component. When customizing them with the `icon` and `checkedIcon` props, the custom icon must render an `<svg>` element — icons rendered as other elements, such as font icons or `<img>`, do not receive the focus indicator.

The indicator hugs whatever box the svg renders at, so smaller replacement icons get a proportionally tighter ring with no extra tuning.

:::success
[`SvgIcon`](/material-ui/icons/#svgicon) is recommended to wrap custom svgs to get consistent styles.
:::

```tsx
import * as React from 'react';
import { createTheme, ThemeProvider } from '@mui/material/styles';
import SvgIcon, { SvgIconProps } from '@mui/material/SvgIcon';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import FormControl from '@mui/material/FormControl';
import FormLabel from '@mui/material/FormLabel';
import FormGroup from '@mui/material/FormGroup';
import FormControlLabel from '@mui/material/FormControlLabel';
import Checkbox from '@mui/material/Checkbox';
import RadioGroup from '@mui/material/RadioGroup';
import Radio from '@mui/material/Radio';

const theme = createTheme({
  focusVisible: true,
  colorSchemes: { light: true, dark: true },
  // These demos opt out of the ripple, so the focus ring is the only keyboard indicator.
  components: { MuiButtonBase: { defaultProps: { disableRipple: true } } },
});

function TightSquareIcon(props: SvgIconProps) {
  return (
    <SvgIcon viewBox="0 0 16 16" {...props} sx={{ fontSize: 16 }}>
      <rect
        x="0.75"
        y="0.75"
        width="14.5"
        height="14.5"
        rx="3.25"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      />
    </SvgIcon>
  );
}

function TightSquareCheckedIcon(props: SvgIconProps) {
  return (
    <SvgIcon viewBox="0 0 16 16" {...props} sx={{ fontSize: 16 }}>
      <rect x="0" y="0" width="16" height="16" rx="4" fill="currentColor" />
      <path
        d="m4.5 8.5 2.5 2.5 4.5-5"
        fill="none"
        stroke="#fff"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </SvgIcon>
  );
}

function TightCircleIcon(props: SvgIconProps) {
  return (
    <SvgIcon viewBox="0 0 16 16" {...props} sx={{ fontSize: 16 }}>
      <circle
        cx="8"
        cy="8"
        r="7.25"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      />
    </SvgIcon>
  );
}

function TightCircleCheckedIcon(props: SvgIconProps) {
  return (
    <SvgIcon viewBox="0 0 16 16" {...props} sx={{ fontSize: 16 }}>
      <circle
        cx="8"
        cy="8"
        r="7.25"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      />
      <circle cx="8" cy="8" r="3.75" fill="currentColor" />
    </SvgIcon>
  );
}

export default function FocusVisibleCustomIcons() {
  return (
    <ThemeProvider theme={theme}>
      <Stack spacing={2}>
        <Typography variant="body2" color="text.secondary">
          Press <kbd>Tab</kbd> — the ring hugs the 16px svg icons.
        </Typography>
        <Stack direction="row" spacing={6}>
          <FormControl component="fieldset">
            <FormLabel component="legend">Settings</FormLabel>
            <FormGroup>
              <FormControlLabel
                control={
                  <Checkbox
                    icon={<TightSquareIcon />}
                    checkedIcon={<TightSquareCheckedIcon />}
                    defaultChecked
                  />
                }
                label="Autosave"
              />
              <FormControlLabel
                control={
                  <Checkbox
                    icon={<TightSquareIcon />}
                    checkedIcon={<TightSquareCheckedIcon />}
                  />
                }
                label="Notifications"
              />
              <FormControlLabel
                control={
                  <Checkbox
                    icon={<TightSquareIcon />}
                    checkedIcon={<TightSquareCheckedIcon />}
                  />
                }
                label="Public profile"
              />
            </FormGroup>
          </FormControl>
          <FormControl>
            <FormLabel id="custom-icons-density-label">Density</FormLabel>
            <RadioGroup
              aria-labelledby="custom-icons-density-label"
              defaultValue="medium"
              name="density"
            >
              <FormControlLabel
                value="compact"
                control={
                  <Radio
                    icon={<TightCircleIcon />}
                    checkedIcon={<TightCircleCheckedIcon />}
                  />
                }
                label="Compact"
              />
              <FormControlLabel
                value="medium"
                control={
                  <Radio
                    icon={<TightCircleIcon />}
                    checkedIcon={<TightCircleCheckedIcon />}
                  />
                }
                label="Medium"
              />
              <FormControlLabel
                value="spacious"
                control={
                  <Radio
                    icon={<TightCircleIcon />}
                    checkedIcon={<TightCircleCheckedIcon />}
                  />
                }
                label="Spacious"
              />
            </RadioGroup>
          </FormControl>
        </Stack>
      </Stack>
    </ThemeProvider>
  );
}

```

### Component focus-visible styles are replaced by the theme

Some components indicate keyboard focus with a translucent background or overlay by default — the `Chip`, `MenuItem`, `ListItemButton`, `AccordionSummary`, `PaginationItem`, `CardActionArea`, `Autocomplete` options, and the `Slider` thumb. When `focusVisible` is enabled, these component focus-visible styles are removed so that the theme's indicator is the only one, consistent across all components — hover, selected, and active styles are unchanged.

### Recomposing a theme with a palette change

Spreading a created theme into `createTheme()` while changing the palette keeps the indicator color resolved from the original palette. Re-pass `focusVisible` in the same call so the color re-derives from the new palette:

```js
const base = createTheme({ focusVisible: true });

// ✅ re-pass focusVisible to re-derive the color from the new palette
createTheme({
  ...base,
  palette: { primary: { main: '#2e7d32' } },
  focusVisible: true,
});
```
