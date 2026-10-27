type OptionValue<Value, MappedValue> = Value | MappedValue | string;
interface UseOptionValueParameters<Value, MappedValue> {
  options: readonly Value[];
  getOptionValue?: ((option: Value) => MappedValue) | undefined;
  isOptionEqualToValue?: ((option: Value, value: OptionValue<Value, MappedValue>) => boolean) | undefined;
  freeSolo: boolean;
  multiple: boolean;
  value: OptionValue<Value, MappedValue> | readonly OptionValue<Value, MappedValue>[] | null;
}
declare const defaultGetOptionValue: <Value>(option: Value) => Value;
/**
 * Maps and matches Autocomplete values using cached option lookups and custom equality when supplied.
 * Current options take precedence over retained selections created by `filterOptions`.
 *
 * Call `rememberSelectedOption` before resetting the input or publishing an option selection.
 * Retained options survive popup closure and are discarded once no longer selected.
 * Matches wrap the original option or free-solo string; unresolved mapped values return `null`.
 */
export default function useOptionValue<Value, MappedValue = never>({
  options,
  getOptionValue: getOptionValueProp,
  isOptionEqualToValue: isOptionEqualToValueProp,
  freeSolo,
  multiple,
  value
}: UseOptionValueParameters<Value, MappedValue>): {
  getOptionValue: ((option: Value) => MappedValue) | typeof defaultGetOptionValue;
  isOptionEqualToValue: (option: Value, value: OptionValue<Value, MappedValue>) => boolean;
  isOptionSelected: (option: Value) => boolean;
  getOptionFromValue: (value: OptionValue<Value, MappedValue>) => {
    option: OptionValue<Value, MappedValue>;
  } | null;
  rememberSelectedOption: (option: Value, optionValue: OptionValue<Value, MappedValue>) => void;
};
export {};