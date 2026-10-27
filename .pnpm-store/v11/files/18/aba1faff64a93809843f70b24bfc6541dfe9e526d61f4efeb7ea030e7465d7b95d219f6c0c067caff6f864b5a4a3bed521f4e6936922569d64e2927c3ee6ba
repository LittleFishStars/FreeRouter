---
productId: material-ui
title: Circular, Linear progress React components
components: CircularProgress, LinearProgress
githubLabel: 'scope: progress'
materialDesign: https://m2.material.io/components/progress-indicators
githubSource: packages/mui-material/src/LinearProgress
---

# Progress

Progress indicators commonly known as spinners, express an unspecified wait time or display the length of a process.

Progress indicators inform users about the status of ongoing processes, such as loading an app, submitting a form, or saving updates.

- **Determinate** indicators display how long an operation will take.
- **Indeterminate** indicators visualize an unspecified wait time.

The animations of the components rely on CSS as much as possible to work even before the JavaScript is loaded.



## Circular

### Circular indeterminate

The default version of CircularProgress renders an indeterminate spinner.

```tsx
import CircularProgress from '@mui/material/CircularProgress';
import Box from '@mui/material/Box';

export default function CircularIndeterminate() {
  return (
    <Box sx={{ display: 'flex' }}>
      <CircularProgress aria-label="Loading…" />
    </Box>
  );
}

```

### Circular color

```tsx
import Stack from '@mui/material/Stack';
import CircularProgress from '@mui/material/CircularProgress';

export default function CircularColor() {
  return (
    <Stack sx={{ color: 'grey.500' }} spacing={2} direction="row">
      <CircularProgress color="secondary" aria-label="Loading…" />
      <CircularProgress color="success" aria-label="Loading…" />
      <CircularProgress color="inherit" aria-label="Loading…" />
    </Stack>
  );
}

```

### Circular size

```tsx
import Stack from '@mui/material/Stack';
import CircularProgress from '@mui/material/CircularProgress';

export default function CircularSize() {
  return (
    <Stack spacing={2} direction="row" sx={{ alignItems: 'center' }}>
      <CircularProgress size="30px" aria-label="Loading…" />
      <CircularProgress size={40} aria-label="Loading…" />
      <CircularProgress size="3rem" aria-label="Loading…" />
    </Stack>
  );
}

```

### Circular determinate

To specify the loading progress of an operation, use the `determinate` value for the `variant` prop. To show the actual progress, use the `value` prop.

```tsx
import * as React from 'react';
import Stack from '@mui/material/Stack';
import CircularProgress from '@mui/material/CircularProgress';

export default function CircularDeterminate() {
  const [progress, setProgress] = React.useState(0);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((prevProgress) => (prevProgress >= 100 ? 0 : prevProgress + 10));
    }, 800);

    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Stack spacing={2} direction="row">
      <CircularProgress variant="determinate" value={25} aria-label="Export data" />
      <CircularProgress variant="determinate" value={50} aria-label="Export data" />
      <CircularProgress variant="determinate" value={75} aria-label="Export data" />
      <CircularProgress variant="determinate" value={100} aria-label="Export data" />
      <CircularProgress
        variant="determinate"
        value={progress}
        aria-label="Export data"
      />
    </Stack>
  );
}

```

### Circular custom scale

By default, progress values are expected in the 0–100 range. You can customize this range by using the `min` and `max` props.

```tsx
import * as React from 'react';
import CircularProgress from '@mui/material/CircularProgress';

export default function CircularCustomScale() {
  const [progress, setProgress] = React.useState(10);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((prevProgress) => (prevProgress >= 20 ? 10 : prevProgress + 2));
    }, 800);

    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <CircularProgress
      variant="determinate"
      min={10}
      max={20}
      value={progress}
      aria-label="Loading"
    />
  );
}

```

### Circular track

To have the circular track always visible, pass the `enableTrackSlot` prop.

```tsx
import * as React from 'react';
import Stack from '@mui/material/Stack';
import CircularProgress from '@mui/material/CircularProgress';

export default function CircularEnableTrack() {
  const [progress, setProgress] = React.useState(0);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((prevProgress) => (prevProgress >= 100 ? 0 : prevProgress + 10));
    }, 800);

    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Stack spacing={2} direction="row">
      <CircularProgress enableTrackSlot size="30px" aria-label="Loading…" />
      <CircularProgress enableTrackSlot size={40} aria-label="Loading…" />
      <CircularProgress enableTrackSlot size="3rem" aria-label="Loading…" />
      <CircularProgress
        enableTrackSlot
        variant="determinate"
        value={70}
        aria-label="Export data"
      />
      <CircularProgress
        enableTrackSlot
        variant="determinate"
        color="secondary"
        value={progress}
        aria-label="Upload photos"
      />
    </Stack>
  );
}

```

### Interactive integration

The following examples show how to integrate the CircularProgress with the Button and FAB components, creating loading states that can be triggered by user actions.

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import CircularProgress from '@mui/material/CircularProgress';
import { green } from '@mui/material/colors';
import Button from '@mui/material/Button';
import Fab from '@mui/material/Fab';
import CheckIcon from '@mui/icons-material/Check';
import SaveIcon from '@mui/icons-material/Save';

export default function CircularIntegration() {
  const [loading, setLoading] = React.useState(false);
  const [success, setSuccess] = React.useState(false);
  const timer = React.useRef<ReturnType<typeof setTimeout>>(undefined);

  const buttonSx = {
    ...(success && {
      bgcolor: green[500],
      '&:hover': {
        bgcolor: green[700],
      },
    }),
  };

  React.useEffect(() => {
    return () => {
      clearTimeout(timer.current);
    };
  }, []);

  const handleButtonClick = () => {
    if (!loading) {
      setSuccess(false);
      setLoading(true);
      timer.current = setTimeout(() => {
        setSuccess(true);
        setLoading(false);
      }, 2000);
    }
  };

  return (
    <Box sx={{ display: 'flex', alignItems: 'center' }}>
      <Box sx={{ m: 1, position: 'relative' }}>
        <Fab
          aria-label="save"
          color="primary"
          sx={buttonSx}
          onClick={handleButtonClick}
        >
          {success ? <CheckIcon /> : <SaveIcon />}
        </Fab>
        {loading && (
          <CircularProgress
            aria-label="Loading…"
            size={68}
            sx={{
              color: green[500],
              position: 'absolute',
              top: -6,
              left: -6,
              zIndex: 1,
            }}
          />
        )}
      </Box>
      <Box sx={{ m: 1, position: 'relative' }}>
        <Button
          variant="contained"
          sx={buttonSx}
          disabled={loading}
          onClick={handleButtonClick}
        >
          Accept terms
        </Button>
        {loading && (
          <CircularProgress
            aria-label="Loading…"
            size={24}
            sx={{
              color: green[500],
              position: 'absolute',
              top: '50%',
              left: '50%',
              marginTop: '-12px',
              marginLeft: '-12px',
            }}
          />
        )}
      </Box>
    </Box>
  );
}

```

### Circular with label

The example shows how to integrate the visual progress value with the CircularProgress component.

```tsx
import * as React from 'react';
import CircularProgress, {
  CircularProgressProps,
} from '@mui/material/CircularProgress';
import Typography from '@mui/material/Typography';
import Box from '@mui/material/Box';

function CircularProgressWithLabel(
  props: CircularProgressProps & { value: number },
) {
  return (
    <Box sx={{ position: 'relative', display: 'inline-flex' }}>
      <CircularProgress
        variant="determinate"
        aria-label="Upload photos"
        {...props}
      />
      <Box
        sx={{
          top: 0,
          left: 0,
          bottom: 0,
          right: 0,
          position: 'absolute',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Typography
          variant="caption"
          component="div"
          sx={{ color: 'text.secondary' }}
        >{`${Math.round(props.value)}%`}</Typography>
      </Box>
    </Box>
  );
}

export default function CircularWithValueLabel() {
  const [progress, setProgress] = React.useState(10);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((prevProgress) => (prevProgress >= 100 ? 0 : prevProgress + 10));
    }, 800);
    return () => {
      clearInterval(timer);
    };
  }, []);

  return <CircularProgressWithLabel value={progress} />;
}

```

## Linear

### Linear indeterminate

LinearProgress shows an indeterminate progress bar by default.

```tsx
import Box from '@mui/material/Box';
import LinearProgress from '@mui/material/LinearProgress';

export default function LinearIndeterminate() {
  return (
    <Box sx={{ width: '100%' }}>
      <LinearProgress aria-label="Loading…" />
    </Box>
  );
}

```

### Linear query

To reverse the direction of the indeterminate animation, use the `query` value for the `variant` prop.

```tsx
import Box from '@mui/material/Box';
import LinearProgress from '@mui/material/LinearProgress';

export default function LinearQuery() {
  return (
    <Box sx={{ width: '100%' }}>
      <LinearProgress aria-label="Loading…" variant="query" />
    </Box>
  );
}

```

### Linear color

```tsx
import Stack from '@mui/material/Stack';
import LinearProgress from '@mui/material/LinearProgress';

export default function LinearColor() {
  return (
    <Stack sx={{ width: '100%', color: 'grey.500' }} spacing={2}>
      <LinearProgress color="secondary" aria-label="Loading…" />
      <LinearProgress color="success" aria-label="Loading…" />
      <LinearProgress color="inherit" aria-label="Loading…" />
    </Stack>
  );
}

```

### Linear determinate

To show the progress on the loading bar, use the `determinate` value for the `variant` prop, along with the `value` prop.

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import LinearProgress from '@mui/material/LinearProgress';

export default function LinearDeterminate() {
  const [progress, setProgress] = React.useState(0);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((oldProgress) => {
        if (oldProgress === 100) {
          return 0;
        }
        const diff = Math.random() * 10;
        return Math.min(oldProgress + diff, 100);
      });
    }, 500);

    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Box sx={{ width: '100%' }}>
      <LinearProgress
        variant="determinate"
        value={progress}
        aria-label="Export data"
      />
    </Box>
  );
}

```

### Linear buffer

Use the `buffer` value for the `variant` prop to show a buffer progress alongside the actual progress value. The `valueBuffer` prop should be greater than the `value` prop.

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import LinearProgress from '@mui/material/LinearProgress';

export default function LinearBuffer() {
  const [progress, setProgress] = React.useState(0);
  const [buffer, setBuffer] = React.useState(10);

  const progressRef = React.useRef(() => {});
  React.useEffect(() => {
    progressRef.current = () => {
      if (progress === 100) {
        setProgress(0);
        setBuffer(10);
      } else {
        setProgress(progress + 1);
        if (buffer < 100 && progress % 5 === 0) {
          const newBuffer = buffer + 1 + Math.random() * 10;
          setBuffer(newBuffer > 100 ? 100 : newBuffer);
        }
      }
    };
  });

  React.useEffect(() => {
    const timer = setInterval(() => {
      progressRef.current();
    }, 100);

    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Box sx={{ width: '100%' }}>
      <LinearProgress
        variant="buffer"
        value={progress}
        valueBuffer={buffer}
        aria-label="Loading…"
      />
    </Box>
  );
}

```

### Linear with label

The progress `value` can also be displayed alongside the progress bar.

```tsx
import * as React from 'react';
import LinearProgress, { LinearProgressProps } from '@mui/material/LinearProgress';
import Typography from '@mui/material/Typography';
import Box from '@mui/material/Box';

function LinearProgressWithLabelAndValue(
  props: LinearProgressProps & { value: number },
) {
  const progressId = React.useId();
  return (
    <div>
      <Typography id={progressId} variant="body2" color="text.secondary">
        Uploading photos…
      </Typography>
      <Box sx={{ display: 'flex', alignItems: 'center' }}>
        <Box sx={{ width: '100%', mr: 1 }}>
          <LinearProgress
            variant="determinate"
            aria-labelledby={progressId}
            {...props}
          />
        </Box>
        <Box sx={{ minWidth: 35 }}>
          <Typography variant="body2" sx={{ color: 'text.secondary' }}>
            {`${Math.round(props.value)}%`}
          </Typography>
        </Box>
      </Box>
    </div>
  );
}

export default function LinearWithValueLabel() {
  const [progress, setProgress] = React.useState(10);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((prevProgress) => (prevProgress >= 100 ? 10 : prevProgress + 10));
    }, 800);
    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Box sx={{ width: '100%' }}>
      <LinearProgressWithLabelAndValue value={progress} />
    </Box>
  );
}

```

### Linear with custom value text

By default, the progress value is read by assistive technology as percentages. Use `aria-valuetext` when the progress value does not involve percentages.

```tsx
import * as React from 'react';
import LinearProgress, { LinearProgressProps } from '@mui/material/LinearProgress';
import Typography from '@mui/material/Typography';
import Box from '@mui/material/Box';

type LinearProgressWithLabelAndValueProps = LinearProgressProps & {
  min: number;
  max: number;
  value: number;
};

function LinearProgressWithLabelAndValue({
  max,
  min,
  value,
  ...rest
}: LinearProgressWithLabelAndValueProps) {
  const progressText = `Elevator at floor ${value} out of ${max}.`;
  const progressId = React.useId();
  return (
    <div>
      <Typography
        id={progressId}
        variant="body2"
        color="text.secondary"
        sx={{ mr: 1 }}
      >
        Elevator status
      </Typography>
      <Box sx={{ display: 'flex', alignItems: 'center' }}>
        <Box sx={{ width: '100%', mr: 1 }}>
          <LinearProgress
            variant="determinate"
            aria-labelledby={progressId}
            aria-valuetext={progressText}
            min={min}
            max={max}
            value={value}
            {...rest}
          />
        </Box>
        <Box sx={{ whiteSpace: 'nowrap' }}>
          <Typography variant="body2" sx={{ color: 'text.secondary' }}>
            {progressText}
          </Typography>
        </Box>
      </Box>
    </div>
  );
}

export default function LinearWithAriaValueText() {
  const [progress, setProgress] = React.useState(1);

  React.useEffect(() => {
    const timer = setInterval(() => {
      setProgress((prevProgress) => (prevProgress >= 10 ? 1 : prevProgress + 1));
    }, 800);
    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Box sx={{ width: '100%' }}>
      <LinearProgressWithLabelAndValue value={progress} min={1} max={10} />
    </Box>
  );
}

```

## Customization

Here are some examples of customizing the component.
You can learn more about this in the [overrides documentation page](/material-ui/customization/how-to-customize/).

```tsx
import * as React from 'react';
import { styled } from '@mui/material/styles';
import Stack from '@mui/material/Stack';
import CircularProgress, {
  circularProgressClasses,
  CircularProgressProps,
} from '@mui/material/CircularProgress';
import LinearProgress, { linearProgressClasses } from '@mui/material/LinearProgress';

const BorderLinearProgress = styled(LinearProgress)(({ theme }) => ({
  height: 10,
  borderRadius: 5,
  [`&.${linearProgressClasses.colorPrimary}`]: {
    backgroundColor: theme.palette.grey[200],
    ...theme.applyStyles('dark', {
      backgroundColor: theme.palette.grey[800],
    }),
  },
  [`& .${linearProgressClasses.bar}`]: {
    borderRadius: 5,
    backgroundColor: '#1a90ff',
    ...theme.applyStyles('dark', {
      backgroundColor: '#308fe8',
    }),
  },
}));

// Inspired by the former Facebook spinners.
function FacebookCircularProgress(props: CircularProgressProps) {
  return (
    <CircularProgress
      variant="indeterminate"
      disableShrink
      enableTrackSlot
      sx={(theme) => ({
        color: '#1a90ff',
        animationDuration: '550ms',
        [`& .${circularProgressClasses.circle}`]: {
          strokeLinecap: 'round',
        },
        [`& .${circularProgressClasses.track}`]: {
          opacity: 1,
          stroke: (theme.vars || theme).palette.grey[200],
          ...theme.applyStyles('dark', {
            stroke: (theme.vars || theme).palette.grey[800],
          }),
        },
        ...theme.applyStyles('dark', {
          color: '#308fe8',
        }),
      })}
      size={40}
      thickness={4}
      aria-label="Loading…"
      {...props}
    />
  );
}

// From https://github.com/mui/material-ui/issues/9496#issuecomment-959408221
function GradientCircularProgress() {
  return (
    <React.Fragment>
      <svg width={0} height={0}>
        <defs>
          <linearGradient id="my_gradient" x1="0%" y1="0%" x2="0%" y2="100%">
            <stop offset="0%" stopColor="#e01cd5" />
            <stop offset="100%" stopColor="#1CB5E0" />
          </linearGradient>
        </defs>
      </svg>
      <CircularProgress
        aria-label="Loading…"
        sx={{ 'svg circle': { stroke: 'url(#my_gradient)' } }}
      />
    </React.Fragment>
  );
}
export default function CustomizedProgressBars() {
  return (
    <Stack spacing={2} sx={{ flexGrow: 1 }}>
      <FacebookCircularProgress />
      <GradientCircularProgress />
      <br />
      <BorderLinearProgress
        variant="determinate"
        value={50}
        aria-label="Export data"
      />
    </Stack>
  );
}

```

## Delaying appearance

There are [3 important limits](https://www.nngroup.com/articles/response-times-3-important-limits/) to know around response time.
The ripple effect of the `ButtonBase` component ensures that the user feels that the UI is reacting instantaneously.
Normally, no special feedback is necessary during delays of more than 0.1 but less than 1.0 second.
After 1.0 second, you can display a loader to keep user's flow of thought uninterrupted.

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import Fade from '@mui/material/Fade';
import Button from '@mui/material/Button';
import CircularProgress from '@mui/material/CircularProgress';
import Typography from '@mui/material/Typography';

export default function DelayingAppearance() {
  const [loading, setLoading] = React.useState(false);
  const [query, setQuery] = React.useState('idle');
  const timerRef = React.useRef<ReturnType<typeof setTimeout>>(undefined);

  React.useEffect(
    () => () => {
      clearTimeout(timerRef.current);
    },
    [],
  );

  const handleClickLoading = () => {
    setLoading((prevLoading) => !prevLoading);
  };

  const handleClickQuery = () => {
    if (timerRef.current) {
      clearTimeout(timerRef.current);
    }

    if (query !== 'idle') {
      setQuery('idle');
      return;
    }

    setQuery('progress');
    timerRef.current = setTimeout(() => {
      setQuery('success');
    }, 2000);
  };

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
      <Box sx={{ height: 40 }}>
        <Fade
          in={loading}
          style={{
            transitionDelay: loading ? '800ms' : '0ms',
          }}
          unmountOnExit
        >
          <CircularProgress aria-label="Loading…" />
        </Fade>
      </Box>
      <Button onClick={handleClickLoading} sx={{ m: 2 }}>
        {loading ? 'Stop loading' : 'Loading'}
      </Button>
      <Box sx={{ height: 40 }}>
        {query === 'success' ? (
          <Typography>Success!</Typography>
        ) : (
          <Fade
            in={query === 'progress'}
            style={{
              transitionDelay: query === 'progress' ? '800ms' : '0ms',
            }}
            unmountOnExit
          >
            <CircularProgress aria-label="Loading…" />
          </Fade>
        )}
      </Box>
      <Button onClick={handleClickQuery} sx={{ m: 2 }}>
        {query !== 'idle' ? 'Reset' : 'Simulate a load'}
      </Button>
    </Box>
  );
}

```

## Accessibility

Progress bars must be given an accessible name by either setting `aria-labelledby` that points to the `id` of a visible text label, or using the `aria-label` attribute.

## Limitations

### High CPU load

Under heavy load, you might lose the stroke dash animation or see random `CircularProgress` ring widths.
You should run processor intensive operations in a web worker or by batch in order not to block the main rendering thread.

<video autoplay muted loop playsinline width="1082" height="158" style="width: 541px;">
  <source src="/static/material-ui/react-components/progress-heavy-load.mp4" type="video/mp4" />
</video>

When it's not possible, you can leverage the `disableShrink` prop to mitigate the issue.
See [this issue](https://github.com/mui/material-ui/issues/10327).

```tsx
import CircularProgress from '@mui/material/CircularProgress';

export default function CircularUnderLoad() {
  return <CircularProgress disableShrink aria-label="Loading…" />;
}

```

### High frequency updates

The `LinearProgress` uses a transition on the CSS transform property to provide a smooth update between different values.
The default transition duration is 200ms.
In the event a parent component updates the `value` prop too quickly, you will at least experience a 200ms delay between the re-render and the progress bar fully updated.

If you need to perform 30 re-renders per second or more, we recommend disabling the transition:

```css
.MuiLinearProgress-bar {
  transition: none;
}
```


# CircularProgress API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Progress](https://mui.com/material-ui/react-progress/)

## Import

```jsx
import CircularProgress from '@mui/material/CircularProgress';
// or
import { CircularProgress } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'inherit' \| 'primary' \| 'secondary' \| 'error' \| 'info' \| 'success' \| 'warning' \| string` | `'primary'` | No |  |
| disableShrink | `bool` | `false` | No |  |
| enableTrackSlot | `bool` | `false` | No |  |
| max | `number` | `100` | No |  |
| min | `number` | `0` | No |  |
| size | `number \| string` | `40` | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| thickness | `number` | `3.6` | No |  |
| value | `number` | `props.min ?? 0` | No |  |
| variant | `'determinate' \| 'indeterminate'` | `'indeterminate'` | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLSpanElement).

> Any other props supplied will be provided to the root element (native element).

## Theme default props

You can use `MuiCircularProgress` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | circle | Styles applied to the `circle` svg path. |
| - | circleDisableShrink | Styles applied to the `circle` svg path if `disableShrink={true}`. |
| - | colorPrimary | Styles applied to the root element if `color="primary"`. |
| - | colorSecondary | Styles applied to the root element if `color="secondary"`. |
| - | determinate | Styles applied to the root element if `variant="determinate"`. |
| - | indeterminate | Styles applied to the root element if `variant="indeterminate"`. |
| - | root | Styles applied to the root element. |
| - | svg | Styles applied to the svg element. |
| - | track | Styles applied to the track slot if `enableTrackSlot={true}`. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/CircularProgress/CircularProgress.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/CircularProgress/CircularProgress.js)

# LinearProgress API

## Demos

For examples and details on the usage of this React component, visit the component demo pages:

- [Progress](https://mui.com/material-ui/react-progress/)

## Import

```jsx
import LinearProgress from '@mui/material/LinearProgress';
// or
import { LinearProgress } from '@mui/material';
```

## Props

| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| classes | `object` | - | No | Override or extend the styles applied to the component. |
| color | `'inherit' \| 'primary' \| 'secondary' \| string` | `'primary'` | No |  |
| max | `number` | `100` | No |  |
| min | `number` | `0` | No |  |
| sx | `Array<func \| object \| bool> \| func \| object` | - | No | The system prop that allows defining system overrides as well as additional CSS styles. |
| value | `number` | - | No |  |
| valueBuffer | `number` | - | No |  |
| variant | `'buffer' \| 'determinate' \| 'indeterminate' \| 'query'` | `'indeterminate'` | No |  |

> **Note**: The `ref` is forwarded to the root element (HTMLSpanElement).

> Any other props supplied will be provided to the root element (native element).

## Theme default props

You can use `MuiLinearProgress` to change the default props of this component with the theme.

## CSS

### Rule name

| Global class | Rule name | Description |
|--------------|-----------|-------------|
| - | bar | Styles applied to the layered bar1 and bar2 elements. |
| - | bar1 | Styles applied to the bar1 element. |
| - | bar2 | Styles applied to the bar2 element. |
| - | buffer | Styles applied to the root element if `variant="buffer"`. |
| - | colorPrimary | Styles applied to the root and bar2 element if `color="primary"`; bar2 if `variant="buffer"`. |
| - | colorSecondary | Styles applied to the root and bar2 elements if `color="secondary"`; bar2 if `variant="buffer"`. |
| - | dashed | Styles applied to the additional bar element if `variant="buffer"`. |
| - | determinate | Styles applied to the root element if `variant="determinate"`. |
| - | indeterminate | Styles applied to the root element if `variant="indeterminate"`. |
| - | query | Styles applied to the root element if `variant="query"`. |
| - | root | Styles applied to the root element. |

## Source code

If you did not find the information on this page, consider having a look at the implementation of the component for more detail.

- [/packages/mui-material/src/LinearProgress/LinearProgress.js](https://github.com/mui/material-ui/tree/HEAD/packages/mui-material/src/LinearProgress/LinearProgress.js)