type OptionValueTypeValidationParams<Option> = {
  options: readonly Option[];
  freeSolo: boolean;
  getOptionValueProp: ((option: Option) => unknown) | undefined;
  duplicatedErrorMessages: Set<string>;
};
/**
 * Validates the option values returned by the `getOptionValue` method of `useAutocomplete`.
 * Ensures that each option has a unique and valid value, and reports errors for duplicates or invalid types.
 */
export default function validateOptionValues<Option>({
  options,
  freeSolo,
  getOptionValueProp,
  duplicatedErrorMessages
}: OptionValueTypeValidationParams<Option>): void;
export declare function validateOptionValue(value: unknown, freeSolo: boolean, duplicatedErrorMessages: Set<string>): void;
export {};