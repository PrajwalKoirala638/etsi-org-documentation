import os  # Import the built-in operating system module to interact with the file system.
import fitz  # Import the PyMuPDF library, which provides tools to read and manipulate PDF files.

# Define a function to check if a PDF file is structurally valid and readable.
def is_pdf_file_valid_and_readable(
    absolute_file_path: str,
) -> bool:  # Accept a string representing the file path and return a boolean result.
    try:  # Start a try block to catch any errors that occur while trying to open the PDF.
        # Use a context manager to open the PDF document, ensuring it closes automatically afterwards to release file locks.
        with fitz.open(
            absolute_file_path
        ) as opened_pdf_document:  # Open the file at the given path and assign it to the variable 'opened_pdf_document'.
            if (
                opened_pdf_document.page_count == 0
            ):  # Check if the total number of pages in the opened PDF document is exactly zero.
                print(
                    f"'{absolute_file_path}' is corrupt or invalid: No pages."
                )  # Print a warning message stating the file has no pages.
                return False  # Return False to indicate that the PDF file is not valid for our purposes.
        return True  # If the file opens successfully and has at least one page, return True to indicate it is valid.
    except RuntimeError as specific_runtime_error:  # Catch PyMuPDF runtime errors that happen when a file is deeply corrupted or not a real PDF.
        print(
            f"Error reading '{absolute_file_path}': {specific_runtime_error}"
        )  # Print the specific runtime error message to the console.
        return False  # Return False because the PDF could not be successfully read.
    except Exception as general_unexpected_error:  # Catch any other unexpected errors, such as operating system IO issues.
        print(
            f"Unexpected error with '{absolute_file_path}': {general_unexpected_error}"
        )  # Print the unexpected error message to the console.
        return False  # Return False to indicate the validation process failed due to an unknown error.


# Define a function that permanently deletes a specific file from the computer's storage.
def delete_file_from_filesystem(
    file_path_to_delete: str,
) -> None:  # Accept the file path to delete as a string and return nothing.
    try:  # Start a try block to handle potential errors during the file deletion process.
        os.remove(
            file_path_to_delete
        )  # Command the operating system to permanently delete the file at the specified path.
        print(
            f"Successfully deleted: {file_path_to_delete}"
        )  # Print a confirmation message letting the user know the file was removed.
    except OSError as operating_system_deletion_error:  # Catch errors specifically related to the operating system failing to delete the file (e.g., file is locked).
        print(
            f"Failed to delete '{file_path_to_delete}': {operating_system_deletion_error}"
        )  # Print an error message explaining why the deletion attempt failed.


# Define a function to search through a main folder and find all files ending with a specific extension (like .pdf).
def find_all_files_with_specific_extension_in_folder(
    target_folder_path: str, desired_file_extension: str
) -> list[str]:  # Accept a folder path and an extension, returning a list of strings.
    list_of_matching_file_paths: list[
        str
    ] = (
        []
    )  # Create an empty list that will eventually hold the full paths of all the files we find.
    desired_file_extension = (
        desired_file_extension.lower()
    )  # Convert the requested file extension to lowercase to ensure our search is case-insensitive.

    for current_folder, sub_folders_inside, files_inside_folder in os.walk(
        target_folder_path
    ):  # Loop through every main folder, sub-folder, and file within the target directory tree.
        for (
            current_file_name
        ) in (
            files_inside_folder
        ):  # Start a nested loop to look at every single individual file name found in the current folder.
            if current_file_name.lower().endswith(
                desired_file_extension
            ):  # Convert the file name to lowercase and check if it ends with the extension we are looking for.
                full_absolute_path_of_file = os.path.abspath(
                    os.path.join(current_folder, current_file_name)
                )  # Combine the folder path and file name to create the complete, absolute file path.
                list_of_matching_file_paths.append(
                    full_absolute_path_of_file
                )  # Add the complete file path to our list of matched files.

    return list_of_matching_file_paths  # Once all folders and files are checked, return the final populated list of file paths.


# Define a function that takes a full file path and isolates just the file's name and its extension.
def extract_file_name_from_full_path(
    complete_file_path: str,
) -> str:  # Accept the full path string and return just the file name as a string.
    return os.path.basename(
        complete_file_path
    )  # Use the operating system module to chop off the folder path and return only the file name at the end.


# Define a function to evaluate if a given text string contains at least one uppercase letter.
def does_string_contain_uppercase_letters(
    text_to_evaluate: str,
) -> bool:  # Accept the text string to check and return a boolean true or false.
    return any(
        individual_character.isupper() for individual_character in text_to_evaluate
    )  # Loop through every character in the string, checking if it is uppercase, and return True if at least one is.


# Define the primary function that coordinates and runs the entire script's workflow.
def execute_main_script_logic():  # Define the main entry point function with no parameters.
    main_directory_to_scan = "./PDFs"  # Create a variable storing the relative path to the folder we want to search for PDFs.

    if not os.path.isdir(
        main_directory_to_scan
    ):  # Check with the operating system if the specified folder path actually exists on the computer.
        print(
            f"Directory '{main_directory_to_scan}' does not exist."
        )  # If the folder doesn't exist, print an error message to the console.
        return  # Exit the main function early since there is no folder to search through.

    discovered_pdf_file_paths = find_all_files_with_specific_extension_in_folder(
        main_directory_to_scan, ".pdf"
    )  # Call our search function to find all PDF files and store the results in a list variable.
    print(
        f"Found {len(discovered_pdf_file_paths)} PDF file(s)."
    )  # Print the total count of PDF files found by checking the length of our results list.

    for (
        current_pdf_file_path
    ) in (
        discovered_pdf_file_paths
    ):  # Start a loop to process every single PDF file path stored in our list one by one.

        if not is_pdf_file_valid_and_readable(
            current_pdf_file_path
        ):  # Call our validation function; if it returns False (meaning the PDF is bad), execute the block below.
            print(
                f"Invalid PDF detected: {current_pdf_file_path}. Deleting file."
            )  # Print a message stating the file is invalid and is about to be deleted.
            delete_file_from_filesystem(
                current_pdf_file_path
            )  # Call our deletion function to remove the corrupted PDF file from the computer.
            continue  # Use the continue keyword to skip the rest of this loop iteration and move directly to the next file in the list.

        isolated_file_name = extract_file_name_from_full_path(
            current_pdf_file_path
        )  # If the file is valid, extract just its name from the full path so we can check its casing.
        if does_string_contain_uppercase_letters(
            isolated_file_name
        ):  # Pass the isolated file name into our uppercase checking function.
            print(
                f"Uppercase letter found in filename: {isolated_file_name}"
            )  # If an uppercase letter is detected, print an informative message to the user.


if (
    __name__ == "__main__"
):  # Check if this Python script is being run directly by the user (rather than being imported as a module by another script).
    execute_main_script_logic()  # If the script is being run directly, execute the main logic function to start the program.
